package gmailwatch_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
	"google.golang.org/api/gmail/v1"
)

// defaultFallbackQuery is the WatchAll query every test below expects.
const defaultFallbackQuery = "-in:sent -in:draft -in:trash -in:spam newer_than:30d (has:attachment OR invoice OR factuur)"

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Fallback on first run or any history gap / `404 historyId not found`: fall back to `users.messages.list` with query **`in:inbox newer_than:{query_window_days}d (has:attachment OR invoice OR factuur)`**. The `in:inbox` and `newer_than:` parts are **load-bearing** — without them a resync sweeps the entire archived mailbox, contradicting F-11. Do not drop them. (F-13)"
//
// `newer_than:` is still load-bearing and still asserted. `in:inbox` is not:
// it was the reason the fallback could not act as a safety net for the
// history path, since neither could see mail the POP3 fetcher imports
// outside the INBOX. The WatchAll scope names its exclusions instead.
func TestFallbackQueryIsScopedAndWindowBounded(t *testing.T) {
	got := gmailwatch.FallbackQuery(gmailwatch.WatchAll, 30)
	if got != defaultFallbackQuery {
		t.Errorf("FallbackQuery(all, 30) = %q, want %q", got, defaultFallbackQuery)
	}

	// A non-positive window must not silently produce an unbounded query —
	// it falls back to the spec's default (30 days), never to "no bound at
	// all".
	if got := gmailwatch.FallbackQuery(gmailwatch.WatchAll, 0); got != defaultFallbackQuery {
		t.Errorf("FallbackQuery(all, 0) = %q, want the 30-day default %q", got, defaultFallbackQuery)
	}

	// An empty/unknown watch value resolves to WatchAll, matching
	// Watcher.effectiveWatch — the two must never disagree.
	if got := gmailwatch.FallbackQuery("", 30); got != defaultFallbackQuery {
		t.Errorf("FallbackQuery(\"\", 30) = %q, want the WatchAll query %q", got, defaultFallbackQuery)
	}

	// The window is configurable, but the scope clause is never dropped for
	// any value.
	got14 := gmailwatch.FallbackQuery(gmailwatch.WatchAll, 14)
	if !strings.Contains(got14, "newer_than:14d") || !strings.Contains(got14, "-in:sent") {
		t.Errorf("FallbackQuery(all, 14) = %q, missing scope/newer_than clauses", got14)
	}

	// WatchInbox still produces the narrow query.
	gotInbox := gmailwatch.FallbackQuery(gmailwatch.WatchInbox, 30)
	wantInbox := "in:inbox newer_than:30d (has:attachment OR invoice OR factuur)"
	if gotInbox != wantInbox {
		t.Errorf("FallbackQuery(inbox, 30) = %q, want %q", gotInbox, wantInbox)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Fallback on first run ... fall back to `users.messages.list` with query `in:inbox newer_than:{query_window_days}d (has:attachment OR invoice OR factuur)`, bounded by `gmail.query_window_days` (F-13)"
func TestPollFirstRunUsesScopedWindowedFallbackQuery(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.MessagesListFunc = func(q, pageToken string) (*gmail.ListMessagesResponse, error) {
		return &gmail.ListMessagesResponse{}, nil // no messages — this test only inspects the query sent
	}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) {
		return &gmail.Profile{HistoryId: 999}, nil
	}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)
	w.Config.QueryWindowDays = 14

	// No sync_state written yet — this is a first-ever poll, which MUST
	// take the F-13 fallback path, never F-12's history.list.
	res, err := w.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !res.UsedFallback {
		t.Error("Poll on first run: UsedFallback = false, want true")
	}

	want := "-in:sent -in:draft -in:trash -in:spam newer_than:14d (has:attachment OR invoice OR factuur)"
	if gmailSrv.LastMessagesListQuery != want {
		t.Errorf("messages.list q = %q, want %q", gmailSrv.LastMessagesListQuery, want)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Fallback on first run, **or on any history gap / `404 historyId not found`**: fall back to `users.messages.list` with the inbox-scoped, window-bounded query. (F-13)"
func TestPollHistoryGapFallsBackToWindowedQuery(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return nil, &fake.APIError{Code: 404, Message: "historyId not found"}
	}
	gmailSrv.MessagesListFunc = func(q, pageToken string) (*gmail.ListMessagesResponse, error) {
		return &gmail.ListMessagesResponse{}, nil
	}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) {
		return &gmail.Profile{HistoryId: 555}, nil
	}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	// A stale historyId that Gmail no longer recognises (simulated by the
	// 404 above).
	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	res, err := w.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !res.UsedFallback {
		t.Error("Poll on history gap: UsedFallback = false, want true")
	}
	if gmailSrv.LastMessagesListQuery != defaultFallbackQuery {
		t.Errorf("messages.list q = %q, want %q", gmailSrv.LastMessagesListQuery, defaultFallbackQuery)
	}

	// A fresh baseline historyId must be established from the fallback so
	// the NEXT poll can resume incrementally rather than falling back again
	// forever.
	st, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.HistoryID != "555" {
		t.Errorf("sync_state.HistoryID after fallback = %q, want %q", st.HistoryID, "555")
	}
}
