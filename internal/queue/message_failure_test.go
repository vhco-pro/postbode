package queue_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

func at(h int) time.Time {
	return time.Date(2026, 8, 25, h, 0, 0, 0, time.UTC)
}

const testCooldown = 6 * time.Hour

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-34: ... polls 1 and 2 return an error and leave sync_state.history_id unchanged; poll 3 parks B"
func TestRecordMessageFailureParksExactlyAtBudget(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	for attempt := 1; attempt <= 2; attempt++ {
		f, parked, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 3, at(attempt), testCooldown)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if parked {
			t.Fatalf("attempt %d parked the message, want parked only at the budget", attempt)
		}
		if f.FailureCount != attempt {
			t.Errorf("attempt %d: FailureCount = %d, want %d", attempt, f.FailureCount, attempt)
		}
		if f.Parked() {
			t.Errorf("attempt %d: Parked() = true, want false", attempt)
		}
	}

	f, parked, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 3, at(3), testCooldown)
	if err != nil {
		t.Fatalf("third attempt: %v", err)
	}
	if !parked {
		t.Fatal("third attempt did not report a park, want parked=true exactly at the budget")
	}
	if !f.Parked() {
		t.Error("third attempt: Parked() = false, want true")
	}
	if f.FailureCount != 3 {
		t.Errorf("FailureCount = %d, want 3", f.FailureCount)
	}
	if f.ParkCount != 1 {
		t.Errorf("ParkCount = %d, want 1", f.ParkCount)
	}
	// The first automatic attempt must be armed at park time (F-75).
	if f.NextRetryAt == nil || !f.NextRetryAt.Equal(at(3).Add(testCooldown)) {
		t.Errorf("NextRetryAt = %v, want %v", f.NextRetryAt, at(3).Add(testCooldown))
	}
	// F-74: the notify-once marker is set by the park itself.
	if f.NotifiedAt == nil {
		t.Error("NotifiedAt is nil after parking, want the notify-once marker set")
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-39: ... Second and third polls that re-encounter the same parked message before its cooldown invoke it zero further times."
func TestRecordMessageFailureReportsParkOnlyOnce(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if _, parked, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 1, at(1), testCooldown); err != nil || !parked {
		t.Fatalf("first failure: parked=%v err=%v, want parked=true", parked, err)
	}
	for attempt := 2; attempt <= 4; attempt++ {
		_, parked, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 1, at(attempt), testCooldown)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if parked {
			t.Fatalf("attempt %d reported a park again; F-74 requires exactly one notification per park", attempt)
		}
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-35: A message whose messages.get fails twice then succeeds is never parked ... and its message_failure row no longer exists."
func TestClearMessageFailureResetsTheCount(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	for attempt := 1; attempt <= 2; attempt++ {
		if _, _, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 3, at(attempt), testCooldown); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if err := db.ClearMessageFailure(ctx, "msg-a"); err != nil {
		t.Fatalf("ClearMessageFailure: %v", err)
	}

	f, err := db.GetMessageFailure(ctx, "msg-a")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if f != nil {
		t.Fatalf("failure row still present after clear: %+v", f)
	}

	// The next failure starts a fresh episode rather than resuming at 3.
	next, parked, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 3, at(5), testCooldown)
	if err != nil {
		t.Fatalf("post-clear failure: %v", err)
	}
	if parked {
		t.Error("a single failure after a clear parked the message; the count did not reset")
	}
	if next.FailureCount != 1 {
		t.Errorf("FailureCount = %d after clear, want 1", next.FailureCount)
	}
}

// Clearing a message that never failed is the overwhelmingly common case —
// every healthy message on every poll — and must not be an error.
func TestClearMessageFailureOnAbsentRowIsNotAnError(t *testing.T) {
	if err := openTestDB(t).ClearMessageFailure(context.Background(), "never-failed"); err != nil {
		t.Fatalf("ClearMessageFailure on absent row: %v", err)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-41: ... postbode retry --all unparks every parked message and reports the count."
func TestDueRetriesAndUnpark(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	for _, id := range []string{"msg-a", "msg-b"} {
		if _, parked, err := db.RecordMessageFailure(ctx, id, "boom", 1, at(1), testCooldown); err != nil || !parked {
			t.Fatalf("park %s: parked=%v err=%v", id, parked, err)
		}
	}

	// Nothing is due before the cooldown elapses.
	due, err := db.DueRetries(ctx, at(2))
	if err != nil {
		t.Fatalf("DueRetries: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("DueRetries before the cooldown = %v, want none", due)
	}

	// Both are due after it.
	due, err = db.DueRetries(ctx, at(1).Add(testCooldown))
	if err != nil {
		t.Fatalf("DueRetries: %v", err)
	}
	if len(due) != 2 {
		t.Errorf("DueRetries after the cooldown = %v, want both ids", due)
	}

	// Unpark makes a message due immediately, without waiting.
	db2 := openTestDB(t)
	if _, parked, err := db2.RecordMessageFailure(ctx, "msg-c", "boom", 1, at(1), testCooldown); err != nil || !parked {
		t.Fatalf("park msg-c: %v", err)
	}
	ok, err := db2.Unpark(ctx, "msg-c", at(2))
	if err != nil {
		t.Fatalf("Unpark: %v", err)
	}
	if !ok {
		t.Error("Unpark reported no change for a parked message")
	}
	due, err = db2.DueRetries(ctx, at(2))
	if err != nil {
		t.Fatalf("DueRetries after unpark: %v", err)
	}
	if len(due) != 1 || due[0] != "msg-c" {
		t.Errorf("DueRetries after unpark = %v, want [msg-c]", due)
	}

	// An unknown or unparked id reports no change, so the CLI can exit
	// non-zero rather than silently claiming success.
	if ok, err := db2.Unpark(ctx, "no-such-message", at(2)); err != nil || ok {
		t.Errorf("Unpark(unknown) = %v, %v; want false, nil", ok, err)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-42: ... after the second retry the message reports auto-retry exhausted while remaining in ListParkedMessages."
func TestRecordRetryAttemptBoundsAutomaticRetries(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if _, parked, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 1, at(0), testCooldown); err != nil || !parked {
		t.Fatalf("park: %v", err)
	}

	// Attempt 1 of 2: still scheduled, at double the base cooldown.
	if err := db.RecordRetryAttempt(ctx, "msg-a", at(1), testCooldown, 2); err != nil {
		t.Fatalf("RecordRetryAttempt 1: %v", err)
	}
	f, err := db.GetMessageFailure(ctx, "msg-a")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if f.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", f.RetryCount)
	}
	if f.NextRetryAt == nil {
		t.Fatal("NextRetryAt is nil after attempt 1 of 2, want the next attempt scheduled")
	}
	if want := at(1).Add(2 * testCooldown); !f.NextRetryAt.Equal(want) {
		t.Errorf("NextRetryAt = %v, want %v (the cooldown doubles)", f.NextRetryAt, want)
	}

	// Attempt 2 of 2 exhausts the budget: no further automatic work, but
	// the message stays parked and stays reported (F-79).
	if err := db.RecordRetryAttempt(ctx, "msg-a", at(2), testCooldown, 2); err != nil {
		t.Fatalf("RecordRetryAttempt 2: %v", err)
	}
	f, err = db.GetMessageFailure(ctx, "msg-a")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if f.NextRetryAt != nil {
		t.Errorf("NextRetryAt = %v after exhausting attempts, want nil", f.NextRetryAt)
	}
	if !f.Parked() {
		t.Error("message is no longer parked after exhausting retries; F-79 requires it stay reported")
	}
	parkedList, err := db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parkedList) != 1 {
		t.Errorf("ListParkedMessages = %d entries, want 1 (exhausted is still parked)", len(parkedList))
	}
	// And nothing is due any more, so it generates no further work.
	if due, err := db.DueRetries(ctx, at(48)); err != nil || len(due) != 0 {
		t.Errorf("DueRetries long after exhaustion = %v (err %v), want none", due, err)
	}
}

// The backoff must not run away: doubling is capped at 24h (F-75).
func TestRetryBackoffIsCappedAtADay(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if _, _, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 1, at(0), testCooldown); err != nil {
		t.Fatalf("park: %v", err)
	}
	prev := time.Time{}
	for i := 1; i <= 8; i++ {
		if err := db.RecordRetryAttempt(ctx, "msg-a", at(0), testCooldown, 100); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		f, err := db.GetMessageFailure(ctx, "msg-a")
		if err != nil {
			t.Fatalf("GetMessageFailure: %v", err)
		}
		if f.NextRetryAt == nil {
			t.Fatalf("attempt %d: NextRetryAt is nil before the attempt bound", i)
		}
		if d := f.NextRetryAt.Sub(at(0)); d > queue.MaxRetryCooldown {
			t.Fatalf("attempt %d: interval %s exceeds the %s cap", i, d, queue.MaxRetryCooldown)
		}
		prev = *f.NextRetryAt
	}
	if want := at(0).Add(queue.MaxRetryCooldown); !prev.Equal(want) {
		t.Errorf("interval settled at %v, want the %s cap at %v", prev, queue.MaxRetryCooldown, want)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-44: Parked state, failure counts, retry schedules and the notify-once markers survive closing and reopening the database"
func TestParkedStateSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	path := dbPath(t, db)

	if _, parked, err := db.RecordMessageFailure(ctx, "msg-a", "boom", 1, at(1), testCooldown); err != nil || !parked {
		t.Fatalf("park: %v", err)
	}
	if err := db.RecordRetryAttempt(ctx, "msg-a", at(2), testCooldown, 3); err != nil {
		t.Fatalf("RecordRetryAttempt: %v", err)
	}
	before, err := db.GetMessageFailure(ctx, "msg-a")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	after, err := reopened.GetMessageFailure(ctx, "msg-a")
	if err != nil {
		t.Fatalf("GetMessageFailure after reopen: %v", err)
	}
	if after == nil {
		t.Fatal("parked state vanished across a reopen")
	}
	if after.FailureCount != before.FailureCount || after.ParkCount != before.ParkCount || after.RetryCount != before.RetryCount {
		t.Errorf("counters changed across reopen: before %+v, after %+v", before, after)
	}
	if after.NotifiedAt == nil {
		t.Error("notify-once marker lost across reopen; a restart would re-notify")
	}
	if !after.NextRetryAt.Equal(*before.NextRetryAt) {
		t.Errorf("NextRetryAt = %v after reopen, want %v — the cooldown restarted", after.NextRetryAt, before.NextRetryAt)
	}
}

// NF-17: a pathological error string must not bloat the database or a
// macOS notification.
func TestLastErrorIsTruncated(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	huge := strings.Repeat("x", 5000)
	f, _, err := db.RecordMessageFailure(ctx, "msg-a", huge, 3, at(1), testCooldown)
	if err != nil {
		t.Fatalf("RecordMessageFailure: %v", err)
	}
	if len([]rune(f.LastError)) > queue.MaxErrorLen+20 {
		t.Errorf("LastError is %d runes, want it clamped near %d", len([]rune(f.LastError)), queue.MaxErrorLen)
	}
	if !strings.Contains(f.LastError, "truncated") {
		t.Error("truncated error does not say so; a reader would mistake it for the whole story")
	}
}
