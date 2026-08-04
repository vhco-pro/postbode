package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "sync_state persistence: history_id, last_poll_at, label_id_submitted, token_issued_at. Crash-safe — a crash mid-poll must not lose or skip messages."
func TestSyncStateRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	got, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState (never written): %v", err)
	}
	if got != (queue.SyncState{}) {
		t.Errorf("GetSyncState (never written) = %+v, want the zero value", got)
	}

	pollAt := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	issuedAt := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	want := queue.SyncState{
		HistoryID:        "123456",
		LastPollAt:       &pollAt,
		LabelIDSubmitted: "Label_2",
		TokenIssuedAt:    &issuedAt,
		LastAuthError:    "",
	}
	if err := db.SaveSyncState(ctx, want); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	got, err = db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if got.HistoryID != want.HistoryID {
		t.Errorf("HistoryID = %q, want %q", got.HistoryID, want.HistoryID)
	}
	if got.LabelIDSubmitted != want.LabelIDSubmitted {
		t.Errorf("LabelIDSubmitted = %q, want %q", got.LabelIDSubmitted, want.LabelIDSubmitted)
	}
	if got.LastPollAt == nil || !got.LastPollAt.Equal(pollAt) {
		t.Errorf("LastPollAt = %v, want %v", got.LastPollAt, pollAt)
	}
	if got.TokenIssuedAt == nil || !got.TokenIssuedAt.Equal(issuedAt) {
		t.Errorf("TokenIssuedAt = %v, want %v", got.TokenIssuedAt, issuedAt)
	}

	// A second save (upsert) must overwrite in place, not duplicate.
	want.LastAuthError = "invalid_grant"
	if err := db.SaveSyncState(ctx, want); err != nil {
		t.Fatalf("SaveSyncState (upsert): %v", err)
	}
	got, err = db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState (after upsert): %v", err)
	}
	if got.LastAuthError != "invalid_grant" {
		t.Errorf("LastAuthError = %q, want %q", got.LastAuthError, "invalid_grant")
	}
}
