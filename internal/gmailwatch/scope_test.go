package gmailwatch_test

import (
	"context"
	"testing"

	"google.golang.org/api/gmail/v1"

	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Watch scope is the whole INBOX (F-11 — the developer chose whole-inbox with rules for precision)."
func TestPollHistorySyncScopesToWholeInbox(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{HistoryId: startHistoryID + 1}, nil
	}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}
	if _, err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if gmailSrv.LastHistoryLabelID != "INBOX" {
		t.Errorf("history.list labelId = %q, want %q (F-11: whole INBOX, not a dedicated label)", gmailSrv.LastHistoryLabelID, "INBOX")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Watch scope is the whole `INBOX`, configurable via `gmail.watch: inbox` (F-11)."
func TestPollHistorySyncHonoursConfiguredWatchScope(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{HistoryId: startHistoryID + 1}, nil
	}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)
	w.Config.Watch = "Inbox" // config value is case-insensitive per spec §6.5

	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}
	if _, err := w.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}

	if gmailSrv.LastHistoryLabelID != "INBOX" {
		t.Errorf("history.list labelId = %q, want %q", gmailSrv.LastHistoryLabelID, "INBOX")
	}
}
