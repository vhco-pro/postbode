package gmailwatch_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
	"google.golang.org/api/gmail/v1"
)

// historyAdding scripts a fake history.list that reports msgID as added.
func historyAdding(msgID string) func(uint64, string) (*gmail.ListHistoryResponse, error) {
	return func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{
			HistoryId: startHistoryID + 1,
			History: []*gmail.History{{
				MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: msgID}}},
			}},
		}, nil
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Watch scope is the whole INBOX (F-11 — the developer chose whole-inbox with rules for precision)."
//
// This test used to assert the OPPOSITE — that history.list is sent
// labelId=INBOX. That parameter was the bug: Gmail matches it against the
// label set recorded in the history record, which is empty for messages
// inserted by the POP3 fetcher, so every imported invoice was dropped before
// its id was ever returned. Verified live against the real mailbox: the same
// window returned 0 messagesAdded with labelId=INBOX and 3 without it, while
// letting [SENT] and [DRAFT] adds through in both cases. The scope now lives
// on the fetched message's real labels (see the tests below), so history.list
// must send no labelId at all.
func TestPollHistorySyncSendsNoLabelIDFilter(t *testing.T) {
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

	if gmailSrv.LastHistoryLabelID != "" {
		t.Errorf("history.list labelId = %q, want %q: the parameter silently drops label-less "+
			"messagesAdded records (POP3-imported mail) and does not enforce the scope it appears to",
			gmailSrv.LastHistoryLabelID, "")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Watch scope is the whole `INBOX`, configurable via `gmail.watch: inbox` (F-11)."
//
// The regression that started all of this: a message Gmail's POP3 fetcher
// imported with "Archive incoming messages" on carries no INBOX label (and,
// in the observed live case, no labels at all). Under the default WatchAll
// scope it must still be processed — it is a real invoice arriving at a real
// mailbox, and dropping it is the silent miss G-1 forbids.
func TestPollProcessesLabellessImportedMessage(t *testing.T) {
	ctx := context.Background()
	const msgID = "msg-pop3-imported"

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.HistoryFunc = historyAdding(msgID)
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		return &gmail.Message{
			Id:           id,
			Raw:          rawMessage(t, "rfc2047-filename.eml"),
			LabelIds:     nil, // exactly what the live mailbox reported
			InternalDate: time.Now().UnixMilli(),
		}, nil
	}

	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)
	w := newTestWatcher(t, svc, db)

	if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}
	res, err := w.Poll(ctx)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if res.StagedCount != 1 {
		t.Errorf("StagedCount = %d, want 1: a message with no INBOX label must still be "+
			"staged under the default %q watch scope", res.StagedCount, gmailwatch.WatchAll)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Watch scope is the whole INBOX (F-11 — the developer chose whole-inbox with rules for precision)."
//
// The other half of the same bug: labelId=INBOX let SENT and DRAFT adds
// through, and nothing downstream re-checked, so Postbode was extracting
// attachments out of the developer's own outgoing mail. No scope admits
// those.
func TestPollSkipsSentDraftTrashAndSpam(t *testing.T) {
	for _, label := range []string{"SENT", "DRAFT", "TRASH", "SPAM"} {
		t.Run(label, func(t *testing.T) {
			ctx := context.Background()
			msgID := "msg-" + strings.ToLower(label)

			gmailSrv := fake.NewServer()
			defer gmailSrv.Close()
			gmailSrv.HistoryFunc = historyAdding(msgID)
			gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
				return &gmail.Message{
					Id:           id,
					Raw:          rawMessage(t, "rfc2047-filename.eml"),
					LabelIds:     []string{label},
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

			if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
				t.Fatalf("SaveSyncState: %v", err)
			}
			res, err := w.Poll(ctx)
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if res.StagedCount != 0 {
				t.Errorf("StagedCount = %d, want 0: %s is outside every watch scope", res.StagedCount, label)
			}

			items, err := db.ItemsByMessageID(ctx, msgID)
			if err != nil {
				t.Fatalf("ItemsByMessageID: %v", err)
			}
			if len(items) != 0 {
				t.Errorf("items = %d, want 0: an out-of-scope message must create no rows at all", len(items))
			}

			// G-1: a skip must be visible, never silent.
			var skipped bool
			for _, line := range logLines {
				if strings.HasPrefix(line, "skip (scope)") {
					skipped = true
				}
			}
			if !skipped {
				t.Errorf("log lines = %v, want one starting with %q", logLines, "skip (scope)")
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Watch scope is the whole `INBOX`, configurable via `gmail.watch: inbox` (F-11)."
//
// The knob still works, and still means what it says: with gmail.watch set
// back to "inbox", a message that does not carry INBOX is skipped.
func TestPollHonoursConfiguredInboxWatchScope(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		labels     []string
		wantStaged int
	}{
		{"archived, no INBOX label", []string{"UNREAD"}, 0},
		{"in the inbox", []string{"UNREAD", "INBOX"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const msgID = "msg-watch-inbox"

			gmailSrv := fake.NewServer()
			defer gmailSrv.Close()
			gmailSrv.HistoryFunc = historyAdding(msgID)
			gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
				return &gmail.Message{
					Id:           id,
					Raw:          rawMessage(t, "rfc2047-filename.eml"),
					LabelIds:     tc.labels,
					InternalDate: time.Now().UnixMilli(),
				}, nil
			}

			db := openTestDB(t)
			svc := gmailServiceFor(t, gmailSrv)
			w := newTestWatcher(t, svc, db)
			w.Config.Watch = "Inbox" // config value is case-insensitive per spec §6.5

			if err := db.SaveSyncState(ctx, queue.SyncState{HistoryID: "1"}); err != nil {
				t.Fatalf("SaveSyncState: %v", err)
			}
			res, err := w.Poll(ctx)
			if err != nil {
				t.Fatalf("Poll: %v", err)
			}
			if res.StagedCount != tc.wantStaged {
				t.Errorf("StagedCount = %d, want %d", res.StagedCount, tc.wantStaged)
			}
		})
	}
}
