package gmailwatch_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
	"google.golang.org/api/gmail/v1"
)

// scriptedMailbox is a fake Gmail whose history.list always returns the same
// ids, and whose messages.get behaviour is decided per id by getFn. It is the
// shape almost every park test needs: a stable listing plus one message that
// misbehaves.
type scriptedMailbox struct {
	srv    *fake.Server
	mu     sync.Mutex
	getHit map[string]int
}

func newScriptedMailbox(t *testing.T, ids []string, getFn func(id string, hit int) (*gmail.Message, error)) *scriptedMailbox {
	t.Helper()
	m := &scriptedMailbox{srv: fake.NewServer(), getHit: map[string]int{}}
	t.Cleanup(m.srv.Close)

	added := make([]*gmail.HistoryMessageAdded, 0, len(ids))
	for _, id := range ids {
		added = append(added, &gmail.HistoryMessageAdded{Message: &gmail.Message{Id: id}})
	}
	m.srv.HistoryFunc = func(uint64, string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{
			HistoryId: 4242,
			History:   []*gmail.History{{MessagesAdded: added}},
		}, nil
	}
	m.srv.MessagesGetFunc = func(id, _ string) (*gmail.Message, error) {
		m.mu.Lock()
		m.getHit[id]++
		hit := m.getHit[id]
		m.mu.Unlock()
		return getFn(id, hit)
	}
	return m
}

// parkTestWatcher wires a watcher with a fake notifier, a captured log and a
// frozen-but-advanceable clock.
type parkTestWatcher struct {
	w        *gmailwatch.Watcher
	db       *queue.DB
	notifier *notify.Fake
	logs     *[]string
	now      *time.Time
}

func newParkTestWatcher(t *testing.T, m *scriptedMailbox, budget int) *parkTestWatcher {
	t.Helper()
	db := openTestDB(t)
	w := newTestWatcher(t, gmailServiceFor(t, m.srv), db)

	n := &notify.Fake{}
	w.Notifier = n

	logs := &[]string{}
	w.Logf = func(format string, args ...any) { *logs = append(*logs, fmt.Sprintf(format, args...)) }

	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	w.Clock = func() time.Time { return now }

	w.Config.FailureBudget = budget

	// Warm historyId so Poll takes the F-12 incremental path.
	if err := db.SaveSyncState(context.Background(), queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}
	return &parkTestWatcher{w: w, db: db, notifier: n, logs: logs, now: &now}
}

func (p *parkTestWatcher) logsContaining(prefix string) []string {
	var out []string
	for _, l := range *p.logs {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (p *parkTestWatcher) historyID(t *testing.T) string {
	t.Helper()
	st, err := p.db.GetSyncState(context.Background())
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	return st.HistoryID
}

func apiError(code int, msg string) error { return &fake.APIError{Code: code, Message: msg} }

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-34: Given a poll listing [A, B, C] where B's messages.get returns 500 on every call: polls 1 and 2 return an error and leave sync_state.history_id unchanged; poll 3 parks B, processes C, and persists sync_state with an advanced history_id."
func TestPollParksAtBudgetAndDrainsTheRest(t *testing.T) {
	ctx := context.Background()
	raw := rawMessage(t, "rfc2047-filename.eml")

	m := newScriptedMailbox(t, []string{"msg-a", "msg-b", "msg-c"}, func(id string, _ int) (*gmail.Message, error) {
		if id == "msg-b" {
			return nil, apiError(http.StatusInternalServerError, "backend error")
		}
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 3)

	// Polls 1 and 2: under budget, so behaviour is unchanged — the cycle
	// aborts and history_id does not move. This is F-71, and it is what
	// lets a transient failure self-heal.
	for i := 1; i <= 2; i++ {
		if _, err := p.w.Poll(ctx); err == nil {
			t.Fatalf("poll %d returned nil error; under budget the poll must still fail", i)
		}
		if got := p.historyID(t); got != "1" {
			t.Fatalf("poll %d advanced history_id to %q; an aborted cycle must not persist progress", i, got)
		}
	}

	// Poll 3 crosses the budget: B is parked, C is processed anyway, and
	// the cycle completes.
	res, err := p.w.Poll(ctx)
	if err != nil {
		t.Fatalf("poll 3 returned an error after parking: %v", err)
	}
	if len(res.Parked) != 1 || res.Parked[0] != "msg-b" {
		t.Errorf("Parked = %v, want [msg-b]", res.Parked)
	}
	if got := p.historyID(t); got != "4242" {
		t.Errorf("history_id = %q after the parking poll, want it advanced to 4242 — this is the whole fix", got)
	}

	// A and C staged; B did not.
	for _, id := range []string{"msg-a", "msg-c"} {
		items, err := p.db.ItemsByMessageID(ctx, id)
		if err != nil {
			t.Fatalf("ItemsByMessageID(%s): %v", id, err)
		}
		if len(items) == 0 {
			t.Errorf("%s staged nothing; a message behind the poison one must still be processed", id)
		}
	}

	parked, err := p.db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parked) != 1 || parked[0].GmailMessageID != "msg-b" {
		t.Fatalf("parked list = %+v, want exactly msg-b", parked)
	}
	if parked[0].FailureCount != 3 {
		t.Errorf("failure count = %d, want 3", parked[0].FailureCount)
	}

	// A fourth poll is quiet: nothing new stages, nothing new parks, no
	// error. The mailbox is healthy again.
	before := p.notifier.Count()
	res4, err := p.w.Poll(ctx)
	if err != nil {
		t.Fatalf("poll 4 errored: %v", err)
	}
	if res4.StagedCount != 0 {
		t.Errorf("poll 4 staged %d, want 0", res4.StagedCount)
	}
	if len(res4.Parked) != 0 {
		t.Errorf("poll 4 parked %v, want nothing new", res4.Parked)
	}
	if p.notifier.Count() != before {
		t.Errorf("poll 4 sent %d extra notification(s); F-74 is notify-once", p.notifier.Count()-before)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-35: A message whose messages.get fails twice then succeeds is never parked: after poll 3 it has staged items, ListParkedMessages is empty, no park notification was sent, and its message_failure row no longer exists."
func TestTransientFailureSelfHealsAndNeverParks(t *testing.T) {
	ctx := context.Background()
	raw := rawMessage(t, "rfc2047-filename.eml")

	m := newScriptedMailbox(t, []string{"msg-flaky"}, func(id string, hit int) (*gmail.Message, error) {
		if hit <= 2 {
			return nil, apiError(http.StatusServiceUnavailable, "temporarily unavailable")
		}
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 3)

	for i := 1; i <= 2; i++ {
		if _, err := p.w.Poll(ctx); err == nil {
			t.Fatalf("poll %d returned nil, want the under-budget abort", i)
		}
	}
	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("poll 3 (the one that succeeds) errored: %v", err)
	}

	items, err := p.db.ItemsByMessageID(ctx, "msg-flaky")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) == 0 {
		t.Error("the recovered message staged nothing")
	}

	parked, err := p.db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parked) != 0 {
		t.Errorf("parked = %+v, want none; two failures then a success must never park", parked)
	}
	if f, err := p.db.GetMessageFailure(ctx, "msg-flaky"); err != nil || f != nil {
		t.Errorf("failure row = %+v (err %v), want it deleted so the count resets", f, err)
	}
	for _, msg := range p.notifier.All() {
		if strings.Contains(msg, "set aside") {
			t.Errorf("a park notification was sent for a message that recovered: %q", msg)
		}
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-39: Parking a message invokes notify.Fake exactly once, with a message containing the gmail_message_id, the failure count and the truncated reason."
func TestParkNotifiesExactlyOnce(t *testing.T) {
	ctx := context.Background()

	m := newScriptedMailbox(t, []string{"msg-poison"}, func(string, int) (*gmail.Message, error) {
		return nil, apiError(http.StatusInternalServerError, "backend error")
	})
	p := newParkTestWatcher(t, m, 1)

	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("parking poll: %v", err)
	}

	parkMsgs := 0
	for _, msg := range p.notifier.All() {
		if strings.Contains(msg, "set aside") {
			parkMsgs++
			if !strings.Contains(msg, "msg-poison") {
				t.Errorf("notification does not name the message id: %q", msg)
			}
			if !strings.Contains(msg, "NOT in the review queue") {
				t.Errorf("notification does not say the message is absent from the review queue: %q", msg)
			}
			if !strings.Contains(msg, "postbode retry msg-poison") {
				t.Errorf("notification does not carry the command that resolves it: %q", msg)
			}
		}
	}
	if parkMsgs != 1 {
		t.Fatalf("park notifications = %d, want exactly 1", parkMsgs)
	}

	// Two more polls re-encounter the parked message and must stay silent.
	for i := 2; i <= 3; i++ {
		if _, err := p.w.Poll(ctx); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}
	after := 0
	for _, msg := range p.notifier.All() {
		if strings.Contains(msg, "set aside") {
			after++
		}
	}
	if after != 1 {
		t.Errorf("park notifications after three polls = %d, want still exactly 1", after)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-36 — regression guard on c92fdb8: A message whose messages.get returns 404 is skipped on the first poll with the existing skip (gone) log line, consumes no budget (no message_failure row is created), is not parked, raises no notification, and the same poll persists sync_state."
func TestGoneMessageConsumesNoBudget(t *testing.T) {
	ctx := context.Background()
	raw := rawMessage(t, "rfc2047-filename.eml")

	m := newScriptedMailbox(t, []string{"msg-gone", "msg-live"}, func(id string, _ int) (*gmail.Message, error) {
		if id == "msg-gone" {
			return nil, apiError(http.StatusNotFound, "Requested entity was not found.")
		}
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 3)

	res, err := p.w.Poll(ctx)
	if err != nil {
		t.Fatalf("poll with a deleted message errored: %v", err)
	}
	if len(res.Parked) != 0 {
		t.Errorf("a 404 parked the message (%v); a dead id must never enter the parked report", res.Parked)
	}
	if f, err := p.db.GetMessageFailure(ctx, "msg-gone"); err != nil || f != nil {
		t.Errorf("a 404 created a failure row %+v (err %v), want none", f, err)
	}
	if got := p.historyID(t); got != "4242" {
		t.Errorf("history_id = %q, want it advanced on the very FIRST poll", got)
	}
	if len(p.logsContaining("skip (gone)")) != 1 {
		t.Errorf("logs = %v, want exactly one skip (gone) line", *p.logs)
	}
	for _, msg := range p.notifier.All() {
		if strings.Contains(msg, "set aside") {
			t.Errorf("a 404 raised a park notification: %q", msg)
		}
	}
}

// A message that was parked and has since been deleted from Gmail leaves the
// parked report: it provably cannot be a silent miss, and leaving it there is
// noise a human can do nothing about.
func TestGoneMessageClearsAnExistingPark(t *testing.T) {
	ctx := context.Background()
	deleted := false

	m := newScriptedMailbox(t, []string{"msg-x"}, func(string, int) (*gmail.Message, error) {
		if deleted {
			return nil, apiError(http.StatusNotFound, "Requested entity was not found.")
		}
		return nil, apiError(http.StatusInternalServerError, "backend error")
	})
	p := newParkTestWatcher(t, m, 1)

	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("parking poll: %v", err)
	}
	if parked, _ := p.db.ListParkedMessages(ctx); len(parked) != 1 {
		t.Fatalf("setup failed: message not parked")
	}

	// The user deletes it. Its retry becomes due, the retry 404s.
	deleted = true
	if _, err := p.db.Unpark(ctx, "msg-x", *p.now); err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("post-deletion poll: %v", err)
	}

	parked, err := p.db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parked) != 0 {
		t.Errorf("parked = %+v, want empty; a deleted message cannot be a silent miss and should not stay reported", parked)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-37: A poll cancelled mid-loop (context cancelled while messages.get is in flight) creates no message_failure row and increments no counter for the in-flight message; the next poll with a live context processes that message normally."
func TestCancelledPollConsumesNoBudget(t *testing.T) {
	raw := rawMessage(t, "rfc2047-filename.eml")

	cancelCtx, cancel := context.WithCancel(context.Background())
	m := newScriptedMailbox(t, []string{"msg-a"}, func(id string, _ int) (*gmail.Message, error) {
		// Cancel while this get is "in flight", then let the transport
		// surface the cancellation.
		cancel()
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 1)

	if _, err := p.w.Poll(cancelCtx); err == nil {
		t.Fatal("a cancelled poll returned nil error")
	}

	f, err := p.db.GetMessageFailure(context.Background(), "msg-a")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if f != nil {
		t.Fatalf("cancellation charged the message: %+v — three restarts would park innocent mail", f)
	}

	// With a live context the same message processes normally.
	if _, err := p.w.Poll(context.Background()); err != nil {
		t.Fatalf("poll after cancellation: %v", err)
	}
	items, err := p.db.ItemsByMessageID(context.Background(), "msg-a")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) == 0 {
		t.Error("the message did not stage on the retry after a cancelled poll")
	}
}
