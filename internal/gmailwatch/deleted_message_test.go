package gmailwatch_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
	"google.golang.org/api/gmail/v1"
)

// A message that history.list reports and messages.get then 404s (deleted
// between the two calls) must not stop the poll: the rest of the batch still
// processes, sync_state still advances, and the next poll therefore does not
// re-list the dead id forever.
//
// Regression: in production a single deleted message wedged the daemon for
// days. The 404 aborted Poll before SaveSyncState, so historyId never moved,
// so the next poll re-listed the same dead id and failed identically — with
// every real invoice that arrived behind it queued in Gmail and never seen.
func TestPollSkipsDeletedMessageAndKeepsGoing(t *testing.T) {
	ctx := context.Background()
	const (
		goneID = "msg-deleted-in-flight"
		liveID = "msg-still-there"
	)
	raw := rawMessage(t, "rfc2047-filename.eml")

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()

	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{
			HistoryId: 99,
			History: []*gmail.History{{
				MessagesAdded: []*gmail.HistoryMessageAdded{
					// The dead id comes FIRST: the bug was order-dependent,
					// so a live message behind it is the whole point.
					{Message: &gmail.Message{Id: goneID}},
					{Message: &gmail.Message{Id: liveID}},
				},
			}},
		}, nil
	}
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		if id == goneID {
			return nil, &fake.APIError{Code: http.StatusNotFound, Message: "Requested entity was not found."}
		}
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	var logLines []string
	w.Logf = func(format string, args ...any) {
		logLines = append(logLines, fmt.Sprintf(format, args...))
	}

	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	res, err := w.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll returned an error for a deleted message: %v", err)
	}
	if res.StagedCount != 1 {
		t.Errorf("StagedCount = %d, want 1 (the live message behind the deleted one)", res.StagedCount)
	}

	items, err := db.ItemsByMessageID(ctx, liveID)
	if err != nil {
		t.Fatalf("ItemsByMessageID(%s): %v", liveID, err)
	}
	if len(items) != 1 {
		t.Errorf("items staged for %s = %d, want 1", liveID, len(items))
	}

	// sync_state must have advanced — that is what stops the next poll from
	// re-listing the dead id.
	st, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.HistoryID != "99" {
		t.Errorf("sync_state.HistoryID = %q, want %q (poll must persist progress past a deleted message)", st.HistoryID, "99")
	}
	if st.LastPollAt == nil {
		t.Error("sync_state.LastPollAt is nil, want the poll recorded as completed")
	}

	found := false
	for _, line := range logLines {
		if strings.HasPrefix(line, "skip (gone)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("log lines = %v, want one starting with %q", logLines, "skip (gone)")
	}
}

// A 500 from messages.get is NOT a deleted message: it may still exist, and
// only a retry can recover it, so Poll must keep failing loudly rather than
// silently skipping past a real invoice (G-1).
func TestPollStillFailsOnNonNotFoundMessageError(t *testing.T) {
	ctx := context.Background()

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()

	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{
			HistoryId: 7,
			History: []*gmail.History{{
				MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "msg-flaky"}}},
			}},
		}, nil
	}
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		return nil, &fake.APIError{Code: http.StatusInternalServerError, Message: "backend error"}
	}

	db := openTestDB(t)
	w := newTestWatcher(t, gmailServiceFor(t, gmailSrv), db)

	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	if _, err := w.Poll(ctx); err == nil {
		t.Fatal("Poll returned nil error for a 500 from messages.get, want a failure")
	}
}
