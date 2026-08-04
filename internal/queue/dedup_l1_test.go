package queue_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "L1 — record every processed `gmail_message_id` before staging; a known id is never re-extracted regardless of history replay, restart or full resync (F-30)"
func TestL1MessageSeenAfterFirstRecord(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	seen, err := db.MessageSeen(ctx, "msg-1")
	if err != nil {
		t.Fatalf("MessageSeen (before recording): %v", err)
	}
	if seen {
		t.Fatal("MessageSeen reported true before the message was ever recorded")
	}

	alreadySeen, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-1", From: "vendor@example.com"})
	if err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	if alreadySeen {
		t.Fatal("RecordMessageIfNew reported alreadySeen=true on the very first record")
	}

	seen, err = db.MessageSeen(ctx, "msg-1")
	if err != nil {
		t.Fatalf("MessageSeen (after recording): %v", err)
	}
	if !seen {
		t.Fatal("MessageSeen reported false immediately after RecordMessageIfNew recorded it")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "L1 — record every processed `gmail_message_id` before staging; a known id is never re-extracted regardless of history replay, restart or full resync (F-30)"
func TestL1SurvivesHistoryReplayRestartAndResync(t *testing.T) {
	ctx := context.Background()

	// "History replay": the same gmailwatch call happens twice within one
	// process run. A caller that gates extraction on RecordMessageIfNew's
	// alreadySeen result stages nothing the second time.
	t.Run("history replay within one run", func(t *testing.T) {
		db := openTestDB(t)

		firstAlreadySeen, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-replay", From: "a@b.com"})
		if err != nil || firstAlreadySeen {
			t.Fatalf("first RecordMessageIfNew: alreadySeen=%v err=%v", firstAlreadySeen, err)
		}
		itemsBefore := stageAndCount(t, db, ctx, "msg-replay", "hash-replay")

		// Replaying the identical history response calls RecordMessageIfNew
		// again with the same id before any extraction would run.
		secondAlreadySeen, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-replay", From: "a@b.com"})
		if err != nil {
			t.Fatalf("second RecordMessageIfNew: %v", err)
		}
		if !secondAlreadySeen {
			t.Fatal("second RecordMessageIfNew (replay) reported alreadySeen=false; a replayed history response must be recognized")
		}
		// A caller respecting alreadySeen never calls StageItem again, so
		// the item count for this message must not have grown.
		itemsAfter := countItemsForMessage(t, db, ctx, "msg-replay")
		if itemsAfter != itemsBefore {
			t.Errorf("item count for msg-replay changed from %d to %d across a replay; L1 must make the replay a total no-op", itemsBefore, itemsAfter)
		}
	})

	// "Restart": a fresh process reopens the same database file and asks
	// about a message id it (the new process) never itself recorded.
	t.Run("restart reopens the same database and still recognizes the id", func(t *testing.T) {
		path := tempDBPath(t)
		db1, err := queue.Open(ctx, path)
		if err != nil {
			t.Fatalf("open (process 1): %v", err)
		}
		if _, err := db1.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-restart", From: "a@b.com"}); err != nil {
			t.Fatalf("RecordMessageIfNew (process 1): %v", err)
		}
		if err := db1.Close(); err != nil {
			t.Fatalf("close (process 1): %v", err)
		}

		db2, err := queue.Open(ctx, path)
		if err != nil {
			t.Fatalf("open (process 2, simulating restart): %v", err)
		}
		defer func() { _ = db2.Close() }()

		seen, err := db2.MessageSeen(ctx, "msg-restart")
		if err != nil {
			t.Fatalf("MessageSeen (process 2): %v", err)
		}
		if !seen {
			t.Fatal("a message recorded before restart was not recognized as seen after reopening the database")
		}
	})

	// "Full resync": the windowed fallback query (F-13) re-lists a message
	// from up to 30 days ago that was already fully processed.
	t.Run("full resync re-lists an already-processed message", func(t *testing.T) {
		db := openTestDB(t)
		if _, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-old", From: "a@b.com"}); err != nil {
			t.Fatalf("initial RecordMessageIfNew: %v", err)
		}
		id := stageItem(t, db, ctx, "msg-old", "hash-old")
		if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := db.MarkUploaded(ctx, id, "uuid-old", 1); err != nil {
			t.Fatalf("MarkUploaded: %v", err)
		}

		// The windowed resync re-lists the message and calls
		// RecordMessageIfNew again before considering extraction.
		alreadySeen, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-old", From: "a@b.com"})
		if err != nil {
			t.Fatalf("RecordMessageIfNew (resync): %v", err)
		}
		if !alreadySeen {
			t.Fatal("a full-resync re-list of an already-processed message was not recognized as already seen")
		}
	})
}

func stageAndCount(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID, sha256 string) int {
	t.Helper()
	stageItem(t, db, ctx, gmailMessageID, sha256)
	return countItemsForMessage(t, db, ctx, gmailMessageID)
}

func countItemsForMessage(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID string) int {
	t.Helper()
	items, err := db.ItemsByMessageID(ctx, gmailMessageID)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	return len(items)
}
