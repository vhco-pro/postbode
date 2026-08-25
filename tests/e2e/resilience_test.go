package e2e_test

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	gwfake "github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
	"google.golang.org/api/gmail/v1"
)

// poison makes messages.get fail for one id, through the real fake Gmail
// server, so the failure enters the pipeline exactly where a real Gmail
// outage would. Returns a func that heals it again.
func (p *pipeline) poison(id string, code int, msg string) (heal func()) {
	original := p.gmailSrv.MessagesGetFunc
	broken := true
	p.gmailSrv.MessagesGetFunc = func(gotID, format string) (m *gmail.Message, err error) {
		if gotID == id && broken {
			return nil, &gwfake.APIError{Code: code, Message: msg}
		}
		return original(gotID, format)
	}
	return func() { broken = false }
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "TestE2EDry_PoisonMessageParksAndMailboxDrains: deliver A, B (500 forever), C; run four polls through the real watcher/extractor/rules/queue wiring; assert A and C reach the review UI as reviewable items, B is parked, sync_state.history_id advanced, exactly one park notification landed in notify.Fake, and cli.FormatStatus output contains the parked messages: section naming B."
func TestE2EDry_PoisonMessageParksAndMailboxDrains(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t)
	p.watcher.Config.FailureBudget = 3

	p.deliver("e2e-a", "billing@vendor-a.example", "Invoice A", []pdfAttachment{{filename: "a.pdf", content: pdfContent("e2e-a")}})
	p.deliver("e2e-b", "billing@vendor-b.example", "Invoice B", []pdfAttachment{{filename: "b.pdf", content: pdfContent("e2e-b")}})
	p.deliver("e2e-c", "billing@vendor-c.example", "Invoice C", []pdfAttachment{{filename: "c.pdf", content: pdfContent("e2e-c")}})
	p.poison("e2e-b", http.StatusInternalServerError, "backend error")

	// Four iterations: three to exhaust B's budget, a fourth to prove the
	// mailbox is quiet afterwards.
	for i := 0; i < 4; i++ {
		p.runIteration(ctx)
	}

	// A and C are reviewable in the real UI, served over a real loopback
	// socket — the reviewer's actual view.
	list := p.getList()
	for _, want := range []string{"vendor-a.example", "vendor-c.example"} {
		if !strings.Contains(list, want) {
			t.Errorf("review queue does not contain %s; a message behind the poison one never drained:\n%s", want, list)
		}
	}
	if strings.Contains(list, "e2e-b") {
		t.Errorf("the parked message appears in the review queue (F-86 forbids it):\n%s", list)
	}

	// B is parked.
	parked, err := p.db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parked) != 1 || parked[0].GmailMessageID != "e2e-b" {
		t.Fatalf("parked = %+v, want exactly e2e-b", parked)
	}
	if parked[0].FailureCount != 3 {
		t.Errorf("failure count = %d, want 3", parked[0].FailureCount)
	}

	// The poll made progress: this is the property the whole feature buys.
	st, err := p.db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.HistoryID == "" {
		t.Error("sync_state.history_id never advanced; the poll is still wedged")
	}
	if !st.PollHealthy() {
		t.Errorf("the daemon still counts as stalled after parking (%d consecutive failures)", st.ConsecutivePollFailures)
	}

	// Exactly one park notification, and no osascript anywhere near it.
	parkNotes := 0
	for _, m := range p.notifier.All() {
		if strings.Contains(m, "set aside") {
			parkNotes++
		}
	}
	if parkNotes != 1 {
		t.Errorf("park notifications = %d, want exactly 1", parkNotes)
	}

	// And it is visible where a human would look.
	report, err := cli.BuildStatusReport(ctx, p.db, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	var status bytes.Buffer
	cli.FormatStatus(&status, report)
	if !strings.Contains(status.String(), "parked messages:  1") {
		t.Errorf("postbode status does not report the parked message:\n%s", status.String())
	}
	if !strings.Contains(status.String(), "e2e-b") {
		t.Errorf("postbode status does not name the parked message:\n%s", status.String())
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "TestE2EDry_ParkedMessageRetriedManuallyReachesQueue: park a message whose failure lands after RecordMessageIfNew, run cli.Retry, poll again, assert its document reaches the queue and can be approved and uploaded through the real form-POST path against the ClearFacts fake. This is AC-45 and AC-41 proven together on the real path."
func TestE2EDry_ParkedMessageRetriedManuallyReachesQueue(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t)
	p.watcher.Config.FailureBudget = 1

	p.deliver("e2e-retry", "billing@vendor-r.example", "Invoice R", []pdfAttachment{{filename: "r.pdf", content: pdfContent("e2e-retry")}})
	heal := p.poison("e2e-retry", http.StatusInternalServerError, "backend error")

	p.runIteration(ctx)
	parked, err := p.db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parked) != 1 {
		t.Fatalf("setup: message not parked (%+v)", parked)
	}

	// The cause clears, and the human runs `postbode retry` — through the
	// real CLI entry point, against the same database the daemon holds
	// open.
	heal()
	var out bytes.Buffer
	if err := cli.Retry(ctx, p.db, &out, "e2e-retry", false, time.Now().UTC()); err != nil {
		t.Fatalf("cli.Retry: %v", err)
	}

	// Nothing in any listing returns this message any more; admission by id
	// is the only route back in.
	p.runIteration(ctx)

	items, err := p.db.ItemsByMessageID(ctx, "e2e-retry")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("the retried message never reached the queue — this is the silent miss AC-45 guards")
	}
	if parkedNow, _ := p.db.ListParkedMessages(ctx); len(parkedNow) != 0 {
		t.Errorf("still parked after a successful retry: %+v", parkedNow)
	}

	// It is a real, reviewable, approvable item: approve it through the
	// actual form POST and let the uploader carry it to the ClearFacts
	// fake.
	var target *queue.Item
	for _, it := range items {
		if it.Status == queue.StatusStaged {
			target = it
			break
		}
	}
	if target == nil {
		t.Fatalf("no staged item to approve among %d row(s)", len(items))
	}
	resp := p.approve(target.ID)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve returned %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	p.runIteration(ctx)

	final, err := p.db.GetItem(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if final.Status != queue.StatusUploaded {
		t.Errorf("item status = %s, want uploaded — a recovered message must be fully usable, not merely present", final.Status)
	}
	if final.UUID == "" {
		t.Error("uploaded item has no uuid from the ClearFacts fake")
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-43: With history.list scripted to 503 on every call and poll_failure_budget: 3 ... postbode status prints poll health: NOT MAKING PROGRESS naming the count and the episode start."
func TestE2EDry_StalledDaemonAnnouncesItselfAndSaysSoInStatus(t *testing.T) {
	ctx := context.Background()
	p := newPipeline(t)
	p.watcher.Config.PollFailureBudget = 3

	// The whole listing fails — nothing to do with any one message. This is
	// the shape the 2026-08-22 outage would have taken if history.list had
	// been the thing that broke.
	p.gmailSrv.MessagesListFunc = func(q, pageToken string) (*gmail.ListMessagesResponse, error) {
		return nil, &gwfake.APIError{Code: http.StatusServiceUnavailable, Message: "backend unavailable"}
	}
	p.gmailSrv.HistoryFunc = func(uint64, string) (*gmail.ListHistoryResponse, error) {
		return nil, &gwfake.APIError{Code: http.StatusServiceUnavailable, Message: "backend unavailable"}
	}

	for i := 0; i < 5; i++ {
		p.runIteration(ctx)
	}

	stallNotes := 0
	for _, m := range p.notifier.All() {
		if strings.Contains(m, "not making progress") {
			stallNotes++
		}
	}
	if stallNotes != 1 {
		t.Errorf("stall notifications after five failing polls = %d, want exactly 1 — the real outage repeated a log line every five minutes and notified nothing", stallNotes)
	}

	report, err := cli.BuildStatusReport(ctx, p.db, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	var status bytes.Buffer
	cli.FormatStatus(&status, report)
	if !strings.Contains(status.String(), "NOT MAKING PROGRESS") {
		t.Errorf("postbode status does not state the stall in words:\n%s", status.String())
	}
}
