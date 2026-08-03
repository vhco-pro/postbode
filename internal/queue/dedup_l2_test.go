package queue_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "L2 — store SHA-256 of every extracted file; a hash matching an item in `staged`/`approved`/`uploaded`/`already_in_portal` produces a `duplicate_linked` row bound via `linked_item_id` with `dedup_layer='L2'`, never uploadable (F-31, AC-11)"
func TestL2LinksByteIdenticalPDFAcrossTwoEmails(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, ctx, "msg-original")
	firstID := stageItem(t, db, ctx, "msg-original", "byte-identical-hash")

	stageMessage(t, db, ctx, "msg-second-email")
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-second-email",
		SpoolPath:      "/spool/second.pdf",
		OrigFilename:   "invoice-again.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      1024,
		SHA256:         "byte-identical-hash",
	})
	if err != nil {
		t.Fatalf("StageItem (second, byte-identical): %v", err)
	}
	if res.Skipped {
		t.Fatalf("StageItem (second, byte-identical): unexpectedly skipped: %s", res.SkipReason)
	}
	if res.Status != queue.StatusDuplicateLinked {
		t.Fatalf("StageItem (second, byte-identical): Status = %s, want %s", res.Status, queue.StatusDuplicateLinked)
	}
	if res.DedupLayer != queue.DedupLayerL2 {
		t.Errorf("DedupLayer = %s, want %s", res.DedupLayer, queue.DedupLayerL2)
	}
	if res.LinkedItemID == nil || *res.LinkedItemID != firstID {
		t.Fatalf("LinkedItemID = %v, want a pointer to %d", res.LinkedItemID, firstID)
	}

	// One uploadable item overall: the first stays staged (uploadable in
	// principle once approved); the second is duplicate_linked and can
	// never itself be approved (staged is the only status Approve accepts
	// as a starting point, and this item was never inserted as staged).
	firstItem, err := db.GetItem(ctx, firstID)
	if err != nil {
		t.Fatalf("GetItem (first): %v", err)
	}
	if firstItem.Status != queue.StatusStaged {
		t.Errorf("first item Status = %s, want %s", firstItem.Status, queue.StatusStaged)
	}

	secondItem, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem (second): %v", err)
	}
	if secondItem.Status != queue.StatusDuplicateLinked {
		t.Errorf("second item Status = %s, want %s", secondItem.Status, queue.StatusDuplicateLinked)
	}
	if secondItem.DedupLayer != queue.DedupLayerL2 {
		t.Errorf("second item DedupLayer = %s, want %s", secondItem.DedupLayer, queue.DedupLayerL2)
	}
	if secondItem.LinkedItemID == nil || *secondItem.LinkedItemID != firstID {
		t.Errorf("second item LinkedItemID = %v, want a pointer to %d", secondItem.LinkedItemID, firstID)
	}

	// duplicate_linked is never a valid starting point for Approve — this
	// is what "never uploadable" means at the lifecycle level (F-41's
	// transition graph has no entry for duplicate_linked at all).
	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err == nil {
		t.Fatal("Approve succeeded on a duplicate_linked item; it must never become uploadable")
	}

	// Visible in the local record: both rows exist and are queryable
	// together by hash.
	byHash, err := db.ItemsBySHA256(ctx, "byte-identical-hash")
	if err != nil {
		t.Fatalf("ItemsBySHA256: %v", err)
	}
	if len(byHash) != 2 {
		t.Fatalf("ItemsBySHA256 returned %d rows, want 2 (original + duplicate_linked)", len(byHash))
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "L2 — store SHA-256 of every extracted file; a hash matching an item in `staged`/`approved`/`uploaded`/`already_in_portal` produces a `duplicate_linked` row bound via `linked_item_id` with `dedup_layer='L2'`, never uploadable (F-31, AC-11)"
//
// This is the OQ-P1 ratification's subtlest edge, proved in both
// directions:
//  1. duplicate_linked sits OUTSIDE the partial unique index predicate, so
//     StageItem storing and linking the byte-identical duplicate does NOT
//     fail — the insert succeeds and the row is fully visible.
//  2. The partial index still guarantees at most one LIVE item per hash: a
//     second attempt to insert an *active*-status row (staged, approved,
//     uploaded, already_in_portal) with the same hash is impossible, both
//     through StageItem's own dedup logic and — checked independently, at
//     the SQL level — through the index itself.
func TestL2DuplicateLinkedSitsOutsidePartialIndexButLiveUniquenessHolds(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, ctx, "msg-a")
	originalID := stageItem(t, db, ctx, "msg-a", "shared-hash")

	stageMessage(t, db, ctx, "msg-b")
	dup, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-b",
		SHA256:         "shared-hash",
		OrigFilename:   "dup.pdf",
	})
	if err != nil {
		// Direction 1: this must NOT be a UNIQUE constraint violation. If it
		// were, duplicate_linked would not actually sit outside the index's
		// predicate, and F-31/AC-11's "stored and linked" requirement would
		// be unimplementable.
		t.Fatalf("direction 1 failed: storing a duplicate_linked row alongside an active row with the same hash returned an error (want success): %v", err)
	}
	if dup.Status != queue.StatusDuplicateLinked {
		t.Fatalf("direction 1: got status %s, want %s — the whole point of duplicate_linked is that this insert succeeds", dup.Status, queue.StatusDuplicateLinked)
	}

	// Direction 1, continued: fetch it back — it truly exists, is not a
	// phantom/rolled-back row.
	stored, err := db.GetItem(ctx, dup.ItemID)
	if err != nil {
		t.Fatalf("direction 1: duplicate_linked row does not exist after a successful StageItem: %v", err)
	}
	if stored.Status != queue.StatusDuplicateLinked || stored.LinkedItemID == nil || *stored.LinkedItemID != originalID {
		t.Fatalf("direction 1: stored row = %+v, want status=duplicate_linked linked_item_id=%d", stored, originalID)
	}

	// Direction 2: the uniqueness guarantee for LIVE items must still hold.
	// A second StageItem call for the same hash produces ANOTHER
	// duplicate_linked row (there is nothing wrong with two duplicates of
	// the same original), never a second staged/approved/uploaded row.
	stageMessage(t, db, ctx, "msg-c")
	dup2, err := db.StageItem(ctx, queue.NewItem{GmailMessageID: "msg-c", SHA256: "shared-hash", OrigFilename: "dup2.pdf"})
	if err != nil {
		t.Fatalf("direction 2 (via StageItem): unexpected error staging a third arrival of the same hash: %v", err)
	}
	if dup2.Status != queue.StatusDuplicateLinked {
		t.Fatalf("direction 2 (via StageItem): third arrival got status %s, want %s — StageItem must never mint a second live row for a hash it already has an active match for", dup2.Status, queue.StatusDuplicateLinked)
	}

	// Direction 2, proved independently at the SQL level: a raw INSERT
	// attempting to give the same hash a second ACTIVE status must be
	// rejected by the partial unique index itself, not merely avoided by
	// StageItem's application-level check.
	raw := rawConn(t, db)
	for _, status := range []queue.Status{queue.StatusStaged, queue.StatusApproved, queue.StatusUploaded, queue.StatusAlreadyInPortal} {
		_, err := raw.ExecContext(ctx, `
			INSERT INTO item (gmail_message_id, sha256, status, staged_at) VALUES (?, ?, ?, ?)
		`, "msg-a", "shared-hash", string(status), "2026-01-01T00:00:00Z")
		if err == nil {
			t.Errorf("direction 2 (raw SQL): inserting a second item with sha256=%q and active status=%q succeeded; the partial unique index must reject it", "shared-hash", status)
		}
	}

	// Exactly one live (non-duplicate_linked, non-terminal-rejected) item
	// exists for this hash, no matter how many duplicate_linked arrivals
	// piled up.
	all, err := db.ItemsBySHA256(ctx, "shared-hash")
	if err != nil {
		t.Fatalf("ItemsBySHA256: %v", err)
	}
	liveCount := 0
	for _, it := range all {
		for _, active := range queue.ActiveDedupStatuses {
			if it.Status == active {
				liveCount++
			}
		}
	}
	if liveCount != 1 {
		t.Fatalf("found %d live items for shared-hash after 3 arrivals, want exactly 1", liveCount)
	}
	if len(all) != 3 {
		t.Fatalf("found %d total items for shared-hash, want 3 (1 live original + 2 duplicate_linked)", len(all))
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "L2 — store SHA-256 of every extracted file; a hash matching an item in `staged`/`approved`/`uploaded`/`already_in_portal` produces a `duplicate_linked` row bound via `linked_item_id` with `dedup_layer='L2'`, never uploadable (F-31, AC-11)"
func TestL2MatchesAgainstEveryActiveStatus(t *testing.T) {
	tests := []struct {
		name          string
		prepareOrigin func(ctx context.Context, db *queue.DB, id int64)
	}{
		{"staged", func(ctx context.Context, db *queue.DB, id int64) {}},
		{"approved", func(ctx context.Context, db *queue.DB, id int64) {
			mustApprove(ctx, db, id)
		}},
		{"uploaded", func(ctx context.Context, db *queue.DB, id int64) {
			mustApprove(ctx, db, id)
			mustUpload(ctx, db, id)
		}},
		{"already_in_portal", func(ctx context.Context, db *queue.DB, id int64) {
			mustAlreadyInPortal(ctx, db, id)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			ctx := context.Background()
			stageMessage(t, db, ctx, "msg-origin")
			originID := stageItem(t, db, ctx, "msg-origin", "hash-"+tt.name)
			tt.prepareOrigin(ctx, db, originID)

			stageMessage(t, db, ctx, "msg-dup")
			res, err := db.StageItem(ctx, queue.NewItem{GmailMessageID: "msg-dup", SHA256: "hash-" + tt.name})
			if err != nil {
				t.Fatalf("StageItem: %v", err)
			}
			if res.Status != queue.StatusDuplicateLinked {
				t.Errorf("against an origin in status %s: got %s, want %s", tt.name, res.Status, queue.StatusDuplicateLinked)
			}
			if res.LinkedItemID == nil || *res.LinkedItemID != originID {
				t.Errorf("against an origin in status %s: LinkedItemID = %v, want %d", tt.name, res.LinkedItemID, originID)
			}
		})
	}
}

func mustApprove(ctx context.Context, db *queue.DB, id int64) {
	if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
		panic(err)
	}
}

func mustUpload(ctx context.Context, db *queue.DB, id int64) {
	if err := db.MarkUploaded(ctx, id, fmt.Sprintf("uuid-for-%d", id), 1); err != nil {
		panic(err)
	}
}

func mustAlreadyInPortal(ctx context.Context, db *queue.DB, id int64) {
	if err := db.MarkAlreadyInPortal(ctx, id, queue.ActorHuman); err != nil {
		panic(err)
	}
}
