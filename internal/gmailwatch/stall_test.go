package gmailwatch_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"
)

func stallNotifications(msgs []string) []string {
	var out []string
	for _, m := range msgs {
		if strings.Contains(m, "not making progress") {
			out = append(out, m)
		}
	}
	return out
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-43: With history.list scripted to 503 on every call and poll_failure_budget: 3: polls 1–2 notify nothing; poll 3 invokes the notifier exactly once naming the consecutive failure count and the last error; polls 4–10 invoke it zero further times"
func TestWholePollStallEscalatesOncePerEpisode(t *testing.T) {
	ctx := context.Background()

	m := newScriptedMailbox(t, []string{"msg-a"}, func(id string, _ int) (*gmail.Message, error) {
		return &gmail.Message{Id: id}, nil
	})
	// history.list itself fails: nothing to do with any one message.
	healthy := false
	m.srv.HistoryFunc = func(uint64, string) (*gmail.ListHistoryResponse, error) {
		if healthy {
			return &gmail.ListHistoryResponse{HistoryId: 4242}, nil
		}
		return nil, apiError(http.StatusServiceUnavailable, "backend unavailable")
	}

	p := newParkTestWatcher(t, m, 3)
	p.w.Config.PollFailureBudget = 3

	for i := 1; i <= 2; i++ {
		if _, err := p.w.Poll(ctx); err == nil {
			t.Fatalf("poll %d returned nil, want a failure", i)
		}
		if n := len(stallNotifications(p.notifier.All())); n != 0 {
			t.Fatalf("poll %d sent %d stall notification(s), want 0 before the budget", i, n)
		}
	}

	if _, err := p.w.Poll(ctx); err == nil {
		t.Fatal("poll 3 returned nil, want a failure")
	}
	notes := stallNotifications(p.notifier.All())
	if len(notes) != 1 {
		t.Fatalf("stall notifications after poll 3 = %d, want exactly 1", len(notes))
	}
	if !strings.Contains(notes[0], "3 consecutive polls") {
		t.Errorf("notification does not name the consecutive failure count: %q", notes[0])
	}
	if !strings.Contains(notes[0], "503") && !strings.Contains(notes[0], "unavailable") {
		t.Errorf("notification does not carry the last error: %q", notes[0])
	}
	if !strings.Contains(notes[0], "postbode status") {
		t.Errorf("notification does not carry the command to run: %q", notes[0])
	}

	// The condition persisting must not turn into a notification storm —
	// this is the failure mode that made the real outage so quiet: a log
	// line every five minutes that nobody reads.
	for i := 4; i <= 10; i++ {
		if _, err := p.w.Poll(ctx); err == nil {
			t.Fatalf("poll %d returned nil, want a failure", i)
		}
	}
	if n := len(stallNotifications(p.notifier.All())); n != 1 {
		t.Errorf("stall notifications after ten failing polls = %d, want still exactly 1", n)
	}

	// Recovery clears the episode...
	healthy = true
	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	st, err := p.db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if !st.PollHealthy() {
		t.Errorf("PollHealthy() = false after recovery (count %d)", st.ConsecutivePollFailures)
	}

	// ...so a NEW stall is a new episode, and notifies again.
	healthy = false
	for i := 1; i <= 3; i++ {
		if _, err := p.w.Poll(ctx); err == nil {
			t.Fatalf("second-episode poll %d returned nil", i)
		}
	}
	if n := len(stallNotifications(p.notifier.All())); n != 2 {
		t.Errorf("stall notifications after a second episode = %d, want 2", n)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-43: ... Separately: a purely per-message stall that ends in a park emits the park notification only — never both — for that cycle."
func TestParkCycleSuppressesTheStallNotification(t *testing.T) {
	ctx := context.Background()

	// One poison message, both budgets at 3: the cycle that parks it is
	// also the cycle that would cross the poll-failure budget.
	m := newScriptedMailbox(t, []string{"msg-poison"}, func(string, int) (*gmail.Message, error) {
		return nil, apiError(http.StatusInternalServerError, "backend error")
	})
	p := newParkTestWatcher(t, m, 3)
	p.w.Config.PollFailureBudget = 3

	for i := 1; i <= 3; i++ {
		if _, err := p.w.Poll(ctx); err != nil && i == 3 {
			t.Fatalf("poll 3 should have parked and succeeded, got: %v", err)
		}
	}

	all := p.notifier.All()
	parkNotes := 0
	for _, msg := range all {
		if strings.Contains(msg, "set aside") {
			parkNotes++
		}
	}
	if parkNotes != 1 {
		t.Errorf("park notifications = %d, want 1", parkNotes)
	}
	if n := len(stallNotifications(all)); n != 0 {
		t.Errorf("stall notifications = %d, want 0 — the park notification is strictly more informative, and parking is what unwedged the poll", n)
	}
}

// A cycle that parks one message AND then aborts under budget on another is
// the only way to reach F-83's rollback branch: it both suppresses the stall
// notification and consumes the episode's one escalation.
//
// The rollback exists so that consumption is undone — otherwise a genuine,
// ongoing stall would stay silent forever, having spent its notification on
// a cycle that never sent one. Found by a coverage gap in review: the branch
// was correct but untested, which is how it would have rotted.
func TestSuppressedStallNotificationIsNotConsumed(t *testing.T) {
	ctx := context.Background()

	// msg-park fails once and parks (budget 1 for previously-parked is not
	// yet relevant; this is its first park). msg-block fails forever too,
	// and because it has never been parked it gets the full budget — so the
	// cycle parks one message and then aborts on the other.
	m := newScriptedMailbox(t, []string{"msg-park", "msg-block"}, func(id string, _ int) (*gmail.Message, error) {
		return nil, apiError(http.StatusInternalServerError, "broken: "+id)
	})
	// Budget 2, so the two messages fall out of step: msg-park is charged
	// first on every cycle and reaches the budget one cycle earlier than
	// msg-block, which is what produces a cycle that parks AND aborts.
	p := newParkTestWatcher(t, m, 2)
	p.w.Config.PollFailureBudget = 2

	// Cycle 1: msg-park fails (1/2) and aborts the cycle. msg-block is
	// never reached. Poll failure count 1, under its budget, so silent.
	if _, err := p.w.Poll(ctx); err == nil {
		t.Fatal("cycle 1 returned nil")
	}
	if n := len(stallNotifications(p.notifier.All())); n != 0 {
		t.Fatalf("cycle 1 notified (%d); it is under the poll budget", n)
	}

	// Cycle 2, the mixed one: msg-park hits 2/2 and PARKS, the loop
	// continues, msg-block fails at 1/2 and aborts. So the cycle both
	// parked something and failed — crossing the poll budget at the same
	// moment. F-83 suppresses the stall notification here.
	res, err := p.w.Poll(ctx)
	if err == nil {
		t.Fatal("cycle 2 returned nil; it should have aborted on msg-block")
	}
	if len(res.Parked) != 1 || res.Parked[0] != "msg-park" {
		t.Fatalf("cycle 2 Parked = %v, want [msg-park] — the mixed park+abort case was not reproduced", res.Parked)
	}
	if n := len(stallNotifications(p.notifier.All())); n != 0 {
		t.Fatalf("cycle 2 sent %d stall notification(s); F-83 suppresses them on a cycle that parked", n)
	}

	// Now break the LISTING itself: a genuine whole-poll stall, unrelated to
	// any message. If the earlier suppressed escalation had been left
	// consumed, this would never announce itself.
	m.srv.HistoryFunc = func(uint64, string) (*gmail.ListHistoryResponse, error) {
		return nil, apiError(http.StatusServiceUnavailable, "backend unavailable")
	}
	if _, err := p.w.Poll(ctx); err == nil {
		t.Fatal("the listing-failure poll returned nil")
	}

	if n := len(stallNotifications(p.notifier.All())); n != 1 {
		t.Errorf("stall notifications = %d after a genuine stall, want 1 — a suppressed escalation must not consume the episode's one notification", n)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-42: With a tiny park_retry_cooldown and park_retry_attempts: 2 (injected clock), a parked message whose failure persists is retried automatically twice and no more; each retry re-parks on the first failure without aborting the poll — every one of those cycles still persists sync_state and still processes the messages behind it"
func TestAutomaticRetryIsBoundedAndNeverRewedgesThePoll(t *testing.T) {
	ctx := context.Background()
	raw := rawMessage(t, "rfc2047-filename.eml")

	m := newScriptedMailbox(t, []string{"msg-poison", "msg-good"}, func(id string, _ int) (*gmail.Message, error) {
		if id == "msg-poison" {
			return nil, apiError(http.StatusInternalServerError, "permanently broken")
		}
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 1) // park on the first failure
	p.w.Config.ParkRetryCooldown = time.Hour
	p.w.Config.ParkRetryAttempts = 2

	// Park it.
	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("parking poll: %v", err)
	}
	if parked, _ := p.db.ListParkedMessages(ctx); len(parked) != 1 {
		t.Fatal("setup: message not parked")
	}

	// Two automatic retries become due, one per cooldown window. Each must
	// fail, re-park, and NOT abort the cycle.
	for attempt := 1; attempt <= 2; attempt++ {
		*p.now = p.now.Add(48 * time.Hour) // well past any backoff
		res, err := p.w.Poll(ctx)
		if err != nil {
			t.Fatalf("retry cycle %d aborted the poll: %v — F-76 requires a re-park to never re-wedge", attempt, err)
		}
		if len(res.Retried) != 1 || res.Retried[0] != "msg-poison" {
			t.Errorf("retry cycle %d admitted %v, want [msg-poison]", attempt, res.Retried)
		}
		// The decisive assertion: the cycle still made progress.
		st, err := p.db.GetSyncState(ctx)
		if err != nil {
			t.Fatalf("GetSyncState: %v", err)
		}
		if st.HistoryID != "4242" {
			t.Errorf("retry cycle %d did not persist sync_state (history_id %q)", attempt, st.HistoryID)
		}
		if !st.PollHealthy() {
			t.Errorf("retry cycle %d left the daemon counted as stalled", attempt)
		}
	}

	// Attempts exhausted: no further automatic work, but still parked and
	// still reported (F-79).
	*p.now = p.now.Add(365 * 24 * time.Hour)
	res, err := p.w.Poll(ctx)
	if err != nil {
		t.Fatalf("post-exhaustion poll: %v", err)
	}
	if len(res.Retried) != 0 {
		t.Errorf("a third automatic retry ran (%v); park_retry_attempts: 2 must bound it", res.Retried)
	}
	parked, err := p.db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parked) != 1 {
		t.Fatalf("parked = %d, want the exhausted message still reported", len(parked))
	}
	if parked[0].NextRetryAt != nil {
		t.Errorf("NextRetryAt = %v after exhaustion, want nil", parked[0].NextRetryAt)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-42: ... A parked message whose failure has healed is processed successfully on its first automatic retry, stages its documents, and its message_failure row disappears."
func TestAutomaticRetryRecoversAHealedMessage(t *testing.T) {
	ctx := context.Background()
	raw := rawMessage(t, "rfc2047-filename.eml")
	broken := true

	m := newScriptedMailbox(t, []string{"msg-heals"}, func(id string, _ int) (*gmail.Message, error) {
		if broken {
			return nil, apiError(http.StatusInternalServerError, "temporarily broken")
		}
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 1)
	p.w.Config.ParkRetryCooldown = time.Hour

	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("parking poll: %v", err)
	}

	// The cause clears; the cooldown elapses.
	broken = false
	*p.now = p.now.Add(2 * time.Hour)
	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}

	items, err := p.db.ItemsByMessageID(ctx, "msg-heals")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) == 0 {
		t.Error("the healed message staged nothing on its automatic retry")
	}
	if f, err := p.db.GetMessageFailure(ctx, "msg-heals"); err != nil || f != nil {
		t.Errorf("failure row = %+v (err %v), want it gone after a successful retry", f, err)
	}
	if parked, _ := p.db.ListParkedMessages(ctx); len(parked) != 0 {
		t.Errorf("still parked after recovery: %+v", parked)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-41: postbode retry <id> on a parked message exits 0, makes the message immediately due for another attempt, and the next poll reprocesses that message and stages its documents even though history_id has advanced past it and no listing returns it."
func TestManualRetryReachesAMessageNoListingReturns(t *testing.T) {
	ctx := context.Background()
	raw := rawMessage(t, "rfc2047-filename.eml")
	broken := true

	m := newScriptedMailbox(t, []string{"msg-x"}, func(id string, _ int) (*gmail.Message, error) {
		if broken {
			return nil, apiError(http.StatusInternalServerError, "broken")
		}
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 1)

	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("parking poll: %v", err)
	}

	// The listing no longer mentions the message at all — exactly what
	// happens in production once history_id has advanced past it.
	m.srv.HistoryFunc = func(uint64, string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{HistoryId: 5000}, nil
	}
	broken = false

	// Without the admission path this poll would do nothing at all.
	if _, err := p.db.Unpark(ctx, "msg-x", *p.now); err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	res, err := p.w.Poll(ctx)
	if err != nil {
		t.Fatalf("post-retry poll: %v", err)
	}
	if len(res.Retried) != 1 {
		t.Fatalf("Retried = %v, want the unparked message admitted by id", res.Retried)
	}

	items, err := p.db.ItemsByMessageID(ctx, "msg-x")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) == 0 {
		t.Error("a manually retried message staged nothing; admission by id is the only way back in once history_id has moved on")
	}
}

// A retried message that fails again must never cost a second full budget
// window (F-76): one failure re-parks it and the cycle continues.
func TestRepeatedlyParkedMessageGetsAnEffectiveBudgetOfOne(t *testing.T) {
	ctx := context.Background()

	m := newScriptedMailbox(t, []string{"msg-poison"}, func(string, int) (*gmail.Message, error) {
		return nil, apiError(http.StatusInternalServerError, "broken")
	})
	// A generous budget of 3 — which must NOT apply on the retry.
	p := newParkTestWatcher(t, m, 3)
	p.w.Config.ParkRetryCooldown = time.Hour

	for i := 1; i <= 3; i++ {
		_, _ = p.w.Poll(ctx)
	}
	if parked, _ := p.db.ListParkedMessages(ctx); len(parked) != 1 {
		t.Fatal("setup: message not parked after three failures")
	}

	*p.now = p.now.Add(2 * time.Hour)
	if _, err := p.w.Poll(ctx); err != nil {
		t.Fatalf("the retry cycle aborted the poll: %v — a re-park must cost exactly one failure, not three", err)
	}

	st, err := p.db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.HistoryID == "1" {
		t.Error("the retry cycle did not persist sync_state; the poll was re-wedged by its own retry")
	}

	f, err := p.db.GetMessageFailure(ctx, "msg-poison")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if f == nil || f.ParkCount < 2 {
		t.Errorf("park count = %+v, want the message re-parked", f)
	}
}

// Cancellation must not be counted as a stall either: quitting the daemon is
// not an outage.
func TestCancelledPollIsNotCountedAsAStall(t *testing.T) {
	raw := rawMessage(t, "rfc2047-filename.eml")
	cancelCtx, cancel := context.WithCancel(context.Background())

	m := newScriptedMailbox(t, []string{"msg-a"}, func(id string, _ int) (*gmail.Message, error) {
		cancel()
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	})
	p := newParkTestWatcher(t, m, 3)

	if _, err := p.w.Poll(cancelCtx); err == nil {
		t.Fatal("cancelled poll returned nil")
	}

	st, err := p.db.GetSyncState(context.Background())
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.ConsecutivePollFailures != 0 {
		t.Errorf("ConsecutivePollFailures = %d after a cancelled poll, want 0", st.ConsecutivePollFailures)
	}
	if n := len(stallNotifications(p.notifier.All())); n != 0 {
		t.Errorf("a cancelled poll produced %d stall notification(s)", n)
	}
}
