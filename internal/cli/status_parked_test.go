package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/queue"
)

func statusOutput(t *testing.T, db *queue.DB, now time.Time) string {
	t.Helper()
	report, err := cli.BuildStatusReport(context.Background(), db, now)
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	var out bytes.Buffer
	cli.FormatStatus(&out, report)
	return out.String()
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-40: With one parked message, postbode status output contains a parked messages: section listing that id, its failure count, its last error and its last attempt time, plus either an auto-retry timestamp or auto-retry exhausted with the exact postbode retry <id> command."
func TestStatusReportsParkedMessages(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Three failures against a budget of three: parked on the third, with a
	// failure count a reader can act on.
	for i := 1; i <= 3; i++ {
		_, ok, err := db.RecordMessageFailure(ctx, "19fe70605192995f", "googleapi: Error 500: backend error", 3, parkedAt(i), 6*time.Hour)
		if err != nil {
			t.Fatalf("failure %d: %v", i, err)
		}
		if ok != (i == 3) {
			t.Fatalf("failure %d reported parked=%v, want %v", i, ok, i == 3)
		}
	}

	got := statusOutput(t, db, parkedAt(4))

	for _, want := range []string{
		"parked messages:  1",
		"19fe70605192995f",
		"failure(s)",
		"backend error",
		"last tried:",
		"next retry:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status output is missing %q:\n%s", want, got)
		}
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-40: ... With none parked it prints parked messages:  0 rather than being omitted."
func TestStatusPrintsAnExplicitZeroForParkedMessages(t *testing.T) {
	got := statusOutput(t, openTestDB(t), parkedAt(4))
	if !strings.Contains(got, "parked messages:  0") {
		t.Errorf("status output omits the parked section when empty; a section that disappears trains the reader not to look for it:\n%s", got)
	}
}

// An exhausted message must name the exact command that revives it — it is
// the only way it will ever be retried again.
func TestStatusNamesTheRetryCommandForAnExhaustedMessage(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if _, _, err := db.RecordMessageFailure(ctx, "msg-dead", "broken", 1, parkedAt(1), time.Hour); err != nil {
		t.Fatalf("park: %v", err)
	}
	// Spend the single allowed automatic attempt.
	if err := db.RecordRetryAttempt(ctx, "msg-dead", parkedAt(2), time.Hour, 1); err != nil {
		t.Fatalf("RecordRetryAttempt: %v", err)
	}

	got := statusOutput(t, db, parkedAt(4))
	if !strings.Contains(got, "auto-retry exhausted") {
		t.Errorf("status does not report the exhausted state:\n%s", got)
	}
	if !strings.Contains(got, "postbode retry msg-dead") {
		t.Errorf("status does not name the exact command that revives it:\n%s", got)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-43: ... postbode status prints poll health: NOT MAKING PROGRESS naming the count and the episode start. Once history.list succeeds, the counter resets, status prints poll health: ok"
func TestStatusStatesPollHealthInWords(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// Healthy: an explicit ok line, not an invitation to subtract
	// timestamps.
	last := parkedAt(3)
	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1", LastPollAt: &last}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}
	if got := statusOutput(t, db, parkedAt(4)); !strings.Contains(got, "poll health:      ok") {
		t.Errorf("healthy status does not say so in words:\n%s", got)
	}

	// Stalled.
	for i := 1; i <= 3; i++ {
		if _, _, err := db.RecordPollFailure(ctx, "history.list: 503 backend unavailable", 3, parkedAt(i)); err != nil {
			t.Fatalf("RecordPollFailure %d: %v", i, err)
		}
	}
	got := statusOutput(t, db, parkedAt(9))
	if !strings.Contains(got, "NOT MAKING PROGRESS") {
		t.Fatalf("stalled status does not say so in words:\n%s", got)
	}
	if !strings.Contains(got, "3 consecutive poll failures") {
		t.Errorf("stalled status does not name the count:\n%s", got)
	}
	if !strings.Contains(got, "503") {
		t.Errorf("stalled status does not carry the last error:\n%s", got)
	}
	// The episode start, not the most recent failure: that is the number
	// that says how bad this is.
	if !strings.Contains(got, "8h ago") && !strings.Contains(got, "8h") {
		t.Errorf("stalled status does not date the episode from its FIRST failure:\n%s", got)
	}

	// The original `last poll:` line is added to, never substituted.
	if !strings.Contains(got, "last poll:") {
		t.Errorf("the existing last poll: line disappeared:\n%s", got)
	}
}
