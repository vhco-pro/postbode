package uploader_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/uploader"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Spool pruning: call `extract.PruneUploadedItems` on a tick after `retention_days`."
func TestPruneSpoolRemovesOnlyUploadsPastRetention(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	itemID := stageApprovedItemWithFile(t, db, ctx, "msg-1", "hash-1")
	if err := db.MarkUploaded(ctx, itemID, "uuid-1", 1); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if _, err := os.Stat(item.SpoolPath); err != nil {
		t.Fatalf("spool file missing before pruning: %v", err)
	}

	start := time.Now().UTC()
	clock := newTestClock(start)
	u := &uploader.Uploader{DB: db, Clock: clock.Now}

	const retentionDays = 30

	// Immediately after upload, well within the 30-day default retention:
	// nothing pruned, the spool file stays.
	prunedCount, errs := u.PruneSpool(ctx, retentionDays)
	if len(errs) != 0 {
		t.Fatalf("PruneSpool (too soon): %v", errs)
	}
	if prunedCount != 0 {
		t.Fatalf("PruneSpool (too soon) pruned %d items, want 0", prunedCount)
	}
	if _, err := os.Stat(item.SpoolPath); err != nil {
		t.Fatalf("spool file removed before retention elapsed: %v", err)
	}

	// Jump the clock 40 days forward — past the 30-day retention window.
	clock.Advance(40 * 24 * time.Hour)

	prunedCount, errs = u.PruneSpool(ctx, retentionDays)
	if len(errs) != 0 {
		t.Fatalf("PruneSpool (past retention): %v", errs)
	}
	if prunedCount != 1 {
		t.Fatalf("PruneSpool (past retention) pruned %d items, want 1", prunedCount)
	}
	if _, err := os.Stat(item.SpoolPath); !os.IsNotExist(err) {
		t.Errorf("spool file still exists after pruning past retention: err = %v, want os.IsNotExist", err)
	}
}
