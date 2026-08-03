package queue_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Rejection memory: a `(gmail_message_id, sha256)` pair once rejected never re-stages (F-44)"
func TestRejectedPairNeverRestages(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	stageMessage(t, db, ctx, "msg-1")
	id := stageItem(t, db, ctx, "msg-1", "hash-a")

	if err := db.Reject(ctx, id, queue.ActorHuman); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	res, err := db.StageItem(ctx, queue.NewItem{GmailMessageID: "msg-1", SHA256: "hash-a"})
	if err != nil {
		t.Fatalf("StageItem (re-attempt of a rejected pair): %v", err)
	}
	if !res.Skipped {
		t.Fatalf("StageItem re-staged a previously rejected (gmail_message_id, sha256) pair: %+v", res)
	}
	if res.ItemID != 0 {
		t.Errorf("Skipped StageItem returned ItemID = %d, want 0 (nothing inserted)", res.ItemID)
	}

	items, err := db.ItemsByMessageID(ctx, "msg-1")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("msg-1 has %d items after a rejected re-attempt, want 1 (only the original, still rejected)", len(items))
	}
	if items[0].Status != queue.StatusRejected {
		t.Errorf("the only item for msg-1 has status %s, want %s", items[0].Status, queue.StatusRejected)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Rejection memory: a `(gmail_message_id, sha256)` pair once rejected never re-stages (F-44)"
func TestRejectionMemoryIsScopedToTheExactPair(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	stageMessage(t, db, ctx, "msg-1")
	id := stageItem(t, db, ctx, "msg-1", "hash-a")
	if err := db.Reject(ctx, id, queue.ActorHuman); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	t.Run("same message, different hash stages normally", func(t *testing.T) {
		res, err := db.StageItem(ctx, queue.NewItem{GmailMessageID: "msg-1", SHA256: "hash-b"})
		if err != nil {
			t.Fatalf("StageItem: %v", err)
		}
		if res.Skipped {
			t.Fatal("a different attachment on the same message was skipped by rejection memory; the memory must be scoped to the exact (message, hash) pair")
		}
	})

	t.Run("different message, same hash stages normally (not an L2 or F-44 match)", func(t *testing.T) {
		stageMessage(t, db, ctx, "msg-2")
		res, err := db.StageItem(ctx, queue.NewItem{GmailMessageID: "msg-2", SHA256: "hash-a"})
		if err != nil {
			t.Fatalf("StageItem: %v", err)
		}
		if res.Skipped {
			t.Fatal("the same file bytes arriving on a different message was skipped by rejection memory; F-44 is scoped to (gmail_message_id, sha256), not sha256 alone")
		}
		// hash-a's only prior appearance is the rejected item, which is not
		// in the active status set, so this is a fresh staged item, not an
		// L2 link either.
		if res.Status != queue.StatusStaged {
			t.Errorf("Status = %s, want %s", res.Status, queue.StatusStaged)
		}
	})
}
