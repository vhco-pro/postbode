package queue_test

import (
	"context"
	"sync"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "`ClaimApproved(ctx)` — select-and-mark inside one transaction so a concurrent or restarted uploader sees zero claimable rows (F-53 foundation, proven in Phase 9)"
func TestClaimApprovedConcurrentGoroutinesExactlyOneWinner(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	itemID := stageAndApprove(t, db, ctx, "msg-1", "hash-1")

	const goroutines = 25
	var wg sync.WaitGroup
	wins := make([]*queue.Item, goroutines)
	errs := make([]error, goroutines)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			item, err := db.ClaimApproved(ctx)
			wins[i] = item
			errs[i] = err
		}(i)
	}
	wg.Wait()

	winners := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: ClaimApproved returned an error: %v", i, err)
		}
		if wins[i] != nil {
			winners++
			if wins[i].ID != itemID {
				t.Errorf("goroutine %d claimed item %d, want %d", i, wins[i].ID, itemID)
			}
		}
	}
	if winners != 1 {
		t.Fatalf("%d of %d concurrent goroutines won the claim on a single approved item, want exactly 1", winners, goroutines)
	}

	item, err := db.GetItem(ctx, itemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.ClaimedAt == nil {
		t.Error("claimed item's ClaimedAt is nil, want set")
	}
	if item.Status != queue.StatusApproved {
		t.Errorf("claimed item Status = %s, want %s (claiming does not itself change status; Phase 9 does that on upload outcome)", item.Status, queue.StatusApproved)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "`ClaimApproved(ctx)` — select-and-mark inside one transaction so a concurrent or restarted uploader sees zero claimable rows (F-53 foundation, proven in Phase 9)"
func TestClaimApprovedSeesZeroRowsAfterAlreadyClaimed(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	stageAndApprove(t, db, ctx, "msg-1", "hash-1")

	first, err := db.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("first ClaimApproved: %v", err)
	}
	if first == nil {
		t.Fatal("first ClaimApproved returned nil, want the single approved item")
	}

	// A second call — standing in for a concurrent uploader instance, or
	// the same uploader restarted and calling ClaimApproved again — must
	// see zero claimable rows, since the only approved item is already
	// claimed.
	second, err := db.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("second ClaimApproved: %v", err)
	}
	if second != nil {
		t.Fatalf("second ClaimApproved returned item %d, want nil (nothing claimable)", second.ID)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "`ClaimApproved(ctx)` — select-and-mark inside one transaction so a concurrent or restarted uploader sees zero claimable rows (F-53 foundation, proven in Phase 9)"
func TestClaimApprovedReturnsNilNotErrorWhenNothingClaimable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	item, err := db.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("ClaimApproved on an empty queue returned an error: %v", err)
	}
	if item != nil {
		t.Fatalf("ClaimApproved on an empty queue returned %+v, want nil", item)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "`ClaimApproved(ctx)` — select-and-mark inside one transaction so a concurrent or restarted uploader sees zero claimable rows (F-53 foundation, proven in Phase 9)"
func TestClaimApprovedPicksOldestFirst(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	older := stageAndApprove(t, db, ctx, "msg-older", "hash-older")
	stageAndApprove(t, db, ctx, "msg-newer", "hash-newer")

	item, err := db.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("ClaimApproved: %v", err)
	}
	if item == nil {
		t.Fatal("ClaimApproved returned nil, want the older of the two approved items")
	}
	if item.ID != older {
		t.Errorf("ClaimApproved picked item %d, want the older item %d", item.ID, older)
	}
}
