package queue

// White-box (package queue, not queue_test): backdating uploaded_at to
// simulate the passage of time needs direct access to the unexported sqlDB
// handle, exactly like crash_safety_internal_test.go.

import (
	"context"
	"testing"
	"time"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Spool pruning: call `extract.PruneUploadedItems` on a tick after `retention_days`. `queue` may need a small additive \"list uploaded items older than N\" query — add it following queue's existing conventions if so."
func TestListUploadedOlderThanFiltersByStatusAndAge(t *testing.T) {
	db := openInternalTestDB(t)
	ctx := context.Background()

	oldID := internalStageAndApprove(t, db, ctx, "msg-old", "hash-old")
	if err := db.MarkUploaded(ctx, oldID, "uuid-old", 1); err != nil {
		t.Fatalf("MarkUploaded(old): %v", err)
	}
	// Backdate uploaded_at directly — MarkUploaded always stamps "now", and
	// the public API has no other way to simulate the passage of 40 days.
	if _, err := db.sqlDB.ExecContext(ctx, `UPDATE item SET uploaded_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-40*24*time.Hour).Format(timeLayout), oldID); err != nil {
		t.Fatalf("backdate uploaded_at: %v", err)
	}

	// A recently uploaded item must not show up in an "older than 15 days"
	// query.
	newID := internalStageAndApprove(t, db, ctx, "msg-new", "hash-new")
	if err := db.MarkUploaded(ctx, newID, "uuid-new", 1); err != nil {
		t.Fatalf("MarkUploaded(new): %v", err)
	}

	// A still-approved item (never uploaded) must never appear either.
	internalStageAndApprove(t, db, ctx, "msg-approved-only", "hash-approved-only")

	cutoff := time.Now().UTC().Add(-15 * 24 * time.Hour)

	items, err := db.ListUploadedOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListUploadedOlderThan: %v", err)
	}
	if len(items) != 1 || items[0].ID != oldID {
		t.Fatalf("ListUploadedOlderThan(%v) = %+v, want exactly [%d]", cutoff, items, oldID)
	}
}

// internalStageAndApprove mirrors testutil_test.go's stageAndApprove for
// white-box (package queue) tests, which cannot import the black-box test
// helpers declared in package queue_test.
func internalStageAndApprove(t *testing.T, db *DB, ctx context.Context, gmailMessageID, sha256 string) int64 {
	t.Helper()
	if _, err := db.RecordMessageIfNew(ctx, Message{GmailMessageID: gmailMessageID, From: "vendor@example.com", Subject: "invoice"}); err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}
	res, err := db.StageItem(ctx, NewItem{
		GmailMessageID: gmailMessageID,
		SpoolPath:      "/spool/" + sha256 + ".pdf",
		OrigFilename:   "invoice.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      1024,
		SHA256:         sha256,
	})
	if err != nil {
		t.Fatalf("StageItem(%s, %s): %v", gmailMessageID, sha256, err)
	}
	if res.Skipped || res.Status != StatusStaged {
		t.Fatalf("StageItem(%s, %s): unexpected result %+v", gmailMessageID, sha256, res)
	}
	if err := db.Approve(ctx, res.ItemID, ActorHuman); err != nil {
		t.Fatalf("Approve(%d): %v", res.ItemID, err)
	}
	return res.ItemID
}
