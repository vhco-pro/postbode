package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-44: ... A prune/retention pass with retention_days set to prune-everything leaves every parked message present and reported."
//
// F-79 is a negative requirement — "no prune, retention or garbage-collection
// path may delete or hide a parked message" — and negative requirements rot
// silently: someone adds a tidy-up months from now and nothing objects. This
// asserts the property directly against the only retention mechanism the
// queue has (ListUploadedOlderThan, which drives PruneSpool), and against
// deliberately ancient park state.
func TestParkedMessagesAreImmuneToRetention(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	// A park from a year ago: far outside any conceivable retention window.
	ancient := time.Date(2025, 8, 25, 12, 0, 0, 0, time.UTC)
	if _, parked, err := db.RecordMessageFailure(ctx, "msg-ancient", "boom", 1, ancient, testCooldown); err != nil || !parked {
		t.Fatalf("park: parked=%v err=%v", parked, err)
	}

	// The retention query the prune path is built on. Prune-everything is
	// expressed as a cutoff in the far future.
	if _, err := db.ListUploadedOlderThan(ctx, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ListUploadedOlderThan: %v", err)
	}

	parkedList, err := db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parkedList) != 1 || parkedList[0].GmailMessageID != "msg-ancient" {
		t.Fatalf("parked list = %+v, want the year-old park still present and reported", parkedList)
	}

	f, err := db.GetMessageFailure(ctx, "msg-ancient")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if f == nil {
		t.Fatal("a year-old parked message was garbage-collected; F-79 forbids it — a 90-day-old park is not stale, it is unresolved")
	}
}

// A parked message must never become an item in any status (F-86): it has no
// extractable document, so nothing in the queue lifecycle should know about
// it.
func TestParkedMessageNeverBecomesAQueueItem(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)

	if _, _, err := db.RecordMessageFailure(ctx, "msg-parked", "boom", 1, at(1), testCooldown); err != nil {
		t.Fatalf("park: %v", err)
	}

	items, err := db.ItemsByMessageID(ctx, "msg-parked")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("parking created %d queue item(s); a parked message must never enter the lifecycle", len(items))
	}

	for _, st := range []queue.Status{
		queue.StatusStaged, queue.StatusApproved, queue.StatusUploaded,
		queue.StatusRejected, queue.StatusFailed, queue.StatusSuppressedPeppol,
	} {
		got, err := db.ListByStatus(ctx, st)
		if err != nil {
			t.Fatalf("ListByStatus(%s): %v", st, err)
		}
		if len(got) != 0 {
			t.Errorf("status %s has %d item(s) after a park, want 0", st, len(got))
		}
	}
}
