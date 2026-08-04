package gmailwatch_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"

	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "F-19: No Gmail state written other than the `VH&Co/submitted` label."
func TestPollWritesNoGmailStateAtAll(t *testing.T) {
	ctx := context.Background()
	raw := rawMessage(t, "rfc2047-filename.eml")

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{
			HistoryId: startHistoryID + 1,
			History: []*gmail.History{{
				MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "msg-no-side-effects"}}},
			}},
		}, nil
	}
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
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

	// Poll must never call users.messages.modify (or anything else that
	// writes Gmail-side state) on its own — the only Gmail write in this
	// package, ApplyLabel, is a caller-invoked primitive Poll never calls
	// itself (F-14's trigger belongs to the Phase 9 uploader).
	if calls := gmailSrv.Calls(); len(calls) != 0 {
		t.Errorf("Poll issued %d users.messages.modify call(s), want 0: %+v", len(calls), calls)
	}
}
