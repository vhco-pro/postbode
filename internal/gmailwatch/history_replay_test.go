package gmailwatch_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"

	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "AC-10: Replaying the same Gmail history response twice produces exactly one set of items; the second pass writes a `skip (L1)` log line and creates zero rows."
func TestPollReplayingSameHistoryResponseTwiceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	const msgID = "msg-history-replay"
	raw := rawMessage(t, "rfc2047-filename.eml")

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()

	// Both polls will ask history.list with whatever startHistoryId the
	// stored sync_state carries; scripting the SAME response regardless of
	// that input is exactly "replaying the same history response twice".
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{
			HistoryId: 42,
			History: []*gmail.History{{
				MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: msgID}}},
			}},
		}, nil
	}
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		return &gmail.Message{
			Id:           id,
			Raw:          raw,
			InternalDate: time.Now().UnixMilli(),
		}, nil
	}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	var logLines []string
	w.Logf = func(format string, args ...any) {
		logLines = append(logLines, fmt.Sprintf(format, args...))
	}

	// Seed a warm historyId so Poll takes the F-12 incremental path, not
	// the F-13 first-run fallback.
	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	res1, err := w.Poll(ctx)
	if err != nil {
		t.Fatalf("first Poll: %v", err)
	}
	if res1.StagedCount != 1 {
		t.Fatalf("first Poll StagedCount = %d, want 1", res1.StagedCount)
	}

	items, err := db.ItemsByMessageID(ctx, msgID)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items after first Poll = %d, want 1", len(items))
	}

	res2, err := w.Poll(ctx)
	if err != nil {
		t.Fatalf("second (replay) Poll: %v", err)
	}
	if res2.StagedCount != 0 {
		t.Errorf("second (replay) Poll StagedCount = %d, want 0", res2.StagedCount)
	}

	itemsAfterReplay, err := db.ItemsByMessageID(ctx, msgID)
	if err != nil {
		t.Fatalf("ItemsByMessageID after replay: %v", err)
	}
	if len(itemsAfterReplay) != 1 {
		t.Fatalf("items after replay Poll = %d, want exactly 1 (zero new rows)", len(itemsAfterReplay))
	}

	found := false
	for _, line := range logLines {
		if strings.HasPrefix(line, "skip (L1)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("log lines = %v, want at least one starting with %q", logLines, "skip (L1)")
	}
}
