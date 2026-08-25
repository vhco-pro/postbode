package queue_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-43: ... polls 1–2 notify nothing; poll 3 invokes the notifier exactly once naming the consecutive failure count and the last error; polls 4–10 invoke it zero further times"
func TestRecordPollFailureEscalatesExactlyOncePerEpisode(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	for i := 1; i <= 2; i++ {
		_, escalate, err := db.RecordPollFailure(ctx, "history.list: 503", 3, at(i))
		if err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if escalate {
			t.Fatalf("failure %d escalated, want escalation only at the budget", i)
		}
	}

	st, escalate, err := db.RecordPollFailure(ctx, "history.list: 503", 3, at(3))
	if err != nil {
		t.Fatalf("failure 3: %v", err)
	}
	if !escalate {
		t.Fatal("failure 3 did not escalate, want exactly one escalation at the budget")
	}
	if st.ConsecutivePollFailures != 3 {
		t.Errorf("ConsecutivePollFailures = %d, want 3", st.ConsecutivePollFailures)
	}
	if st.FirstPollFailureAt == nil || !st.FirstPollFailureAt.Equal(at(1)) {
		t.Errorf("FirstPollFailureAt = %v, want the FIRST failure at %v", st.FirstPollFailureAt, at(1))
	}

	for i := 4; i <= 10; i++ {
		if _, escalate, err := db.RecordPollFailure(ctx, "history.list: 503", 3, at(i)); err != nil || escalate {
			t.Fatalf("failure %d escalated again (err %v); F-82 requires one notification per episode", i, err)
		}
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-43: Once history.list succeeds, the counter resets, status prints poll health: ok, and a subsequent new stall episode notifies again exactly once."
func TestClearPollFailureEndsTheEpisodeSoALaterStallNotifiesAgain(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	for i := 1; i <= 3; i++ {
		if _, _, err := db.RecordPollFailure(ctx, "503", 3, at(i)); err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
	}

	// A successful poll clears the episode as part of persisting sync_state.
	st, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	db.ClearPollFailure(&st)
	if err := db.SaveSyncState(ctx, st); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	reread, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState after clear: %v", err)
	}
	if !reread.PollHealthy() {
		t.Errorf("PollHealthy() = false after a successful poll, want true (count %d)", reread.ConsecutivePollFailures)
	}
	if reread.FirstPollFailureAt != nil || reread.PollStallNotifiedAt != nil || reread.LastPollError != "" {
		t.Errorf("episode state survived the clear: %+v", reread)
	}

	// A NEW episode escalates again — the point of clearing the marker.
	for i := 4; i <= 5; i++ {
		if _, escalate, err := db.RecordPollFailure(ctx, "503 again", 3, at(i)); err != nil || escalate {
			t.Fatalf("new-episode failure %d: escalate=%v err=%v, want no escalation yet", i, escalate, err)
		}
	}
	if _, escalate, err := db.RecordPollFailure(ctx, "503 again", 3, at(6)); err != nil || !escalate {
		t.Fatalf("second episode did not escalate (escalate=%v err=%v); a later stall must notify again", escalate, err)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-44: ... reopening does not re-notify, does not reset any counter"
func TestPollStallStateSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	path := dbPath(t, db)

	for i := 1; i <= 3; i++ {
		if _, _, err := db.RecordPollFailure(ctx, "503", 3, at(i)); err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	// A daemon restarted mid-stall must not re-announce a known problem.
	_, escalate, err := reopened.RecordPollFailure(ctx, "503", 3, at(4))
	if err != nil {
		t.Fatalf("RecordPollFailure after reopen: %v", err)
	}
	if escalate {
		t.Error("a restart mid-stall re-escalated; the notify-once marker did not survive")
	}
	st, err := reopened.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.ConsecutivePollFailures != 4 {
		t.Errorf("ConsecutivePollFailures = %d after reopen, want the counter to have continued to 4", st.ConsecutivePollFailures)
	}
}

// RecordPollFailure records the failure without recording progress that did
// not happen: history_id and last_poll_at must be untouched, since those are
// exactly what `postbode status` reads to report staleness.
func TestRecordPollFailureDoesNotAdvanceProgressFields(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	seeded := at(1)
	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "4242", LastPollAt: &seeded}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}
	if _, _, err := db.RecordPollFailure(ctx, "503", 3, at(9)); err != nil {
		t.Fatalf("RecordPollFailure: %v", err)
	}

	st, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.HistoryID != "4242" {
		t.Errorf("HistoryID = %q, want it untouched at %q", st.HistoryID, "4242")
	}
	if st.LastPollAt == nil || !st.LastPollAt.Equal(seeded) {
		t.Errorf("LastPollAt = %v, want it untouched at %v — a failed poll made no progress", st.LastPollAt, seeded)
	}
}
