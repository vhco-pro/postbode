package queue_test

// Verifies the small additive queue.DB methods Phase 9 (the uploader) needs
// on top of the Phase 4 foundation: ClaimApprovedDue's backoff-aware claim
// filter, RecordUploadRetry/MarkVerified's non-transition column writes,
// ReleaseStaleClaims' crash recovery, MarkMessageSubmitted's F-14 timestamp
// pair, and ListUploadedOlderThan's F-24 pruning query.

import (
	"context"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Claim-in-transaction before every upload, re-checking layers L1–L3 inside the same transaction so a concurrent or restarted uploader cannot double-send (F-53, AC-18)"
func TestClaimApprovedDueSkipsItemNotYetDue(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	id := stageAndApprove(t, db, ctx, "msg-1", "hash-1")
	if err := db.RecordUploadRetry(ctx, id, "503", base.Add(10*time.Minute), base); err != nil {
		t.Fatalf("RecordUploadRetry: %v", err)
	}

	// Not due yet: now is before next_retry_at.
	item, err := db.ClaimApprovedDue(ctx, base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("ClaimApprovedDue (not yet due): %v", err)
	}
	if item != nil {
		t.Fatalf("ClaimApprovedDue claimed item %d before its next_retry_at, want nil", item.ID)
	}

	// Due now.
	item, err = db.ClaimApprovedDue(ctx, base.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("ClaimApprovedDue (due): %v", err)
	}
	if item == nil || item.ID != id {
		t.Fatalf("ClaimApprovedDue at next_retry_at = %+v, want item %d", item, id)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Claim-in-transaction before every upload, re-checking layers L1–L3 inside the same transaction so a concurrent or restarted uploader cannot double-send (F-53, AC-18)"
func TestClaimApprovedDueClaimsNeverFailedItemImmediately(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id := stageAndApprove(t, db, ctx, "msg-1", "hash-1")

	item, err := db.ClaimApprovedDue(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClaimApprovedDue: %v", err)
	}
	if item == nil || item.ID != id {
		t.Fatalf("ClaimApprovedDue = %+v, want item %d (never failed, no backoff pending)", item, id)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Retry persistence: exponential backoff 1m→2h, give up after 24h into `failed` with the last error stored; 4xx except 429 is terminal-`failed` immediately with `retry_count == 0` (F-51, AC-17)"
func TestRecordUploadRetryIncrementsAndReleasesClaim(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id := stageAndApprove(t, db, ctx, "msg-1", "hash-1")

	claimed, err := db.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("ClaimApproved: %v", err)
	}
	if claimed == nil || claimed.ID != id {
		t.Fatalf("ClaimApproved = %+v, want item %d", claimed, id)
	}

	now := time.Now().UTC()
	next := now.Add(time.Minute)
	if err := db.RecordUploadRetry(ctx, id, "503 service unavailable", next, now); err != nil {
		t.Fatalf("RecordUploadRetry: %v", err)
	}

	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusApproved {
		t.Errorf("Status = %s after a retryable failure, want unchanged %s (F-52 durable approval)", item.Status, queue.StatusApproved)
	}
	if item.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", item.RetryCount)
	}
	if item.LastError != "503 service unavailable" {
		t.Errorf("LastError = %q, want %q", item.LastError, "503 service unavailable")
	}
	if item.NextRetryAt == nil || !item.NextRetryAt.Equal(next) {
		t.Errorf("NextRetryAt = %v, want %v", item.NextRetryAt, next)
	}
	if item.ClaimedAt != nil {
		t.Error("ClaimedAt is still set after RecordUploadRetry, want released (nil) so the item is claimable again")
	}
	if item.FirstFailedAt == nil {
		t.Error("FirstFailedAt is nil after the first retryable failure, want set")
	}

	// A second failure must not move FirstFailedAt.
	firstFailedAt := *item.FirstFailedAt
	if err := db.RecordUploadRetry(ctx, id, "503 again", next.Add(2*time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatalf("RecordUploadRetry (second): %v", err)
	}
	item, err = db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.RetryCount != 2 {
		t.Errorf("RetryCount after second failure = %d, want 2", item.RetryCount)
	}
	if item.FirstFailedAt == nil || !item.FirstFailedAt.Equal(firstFailedAt) {
		t.Errorf("FirstFailedAt changed on a second failure: got %v, want unchanged %v", item.FirstFailedAt, firstFailedAt)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Proof of delivery: store the returned `uuid`, then call `document(id:)` and store `verified_at`; an item with a `uuid` but no `verified_at` displays as `uploaded (unverified)` and is **never retried** ... (F-37). See ADR-003."
func TestMarkVerifiedSetsVerifiedAtWithoutChangingStatus(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id := stageAndApprove(t, db, ctx, "msg-1", "hash-1")
	if err := db.MarkUploaded(ctx, id, "uuid-1", 2); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}

	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.VerifiedAt != nil {
		t.Fatal("VerifiedAt is set immediately after MarkUploaded, want nil until MarkVerified runs")
	}

	verifiedAt := time.Now().UTC()
	if err := db.MarkVerified(ctx, id, verifiedAt); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}

	item, err = db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusUploaded {
		t.Errorf("Status = %s after MarkVerified, want unchanged %s", item.Status, queue.StatusUploaded)
	}
	if item.VerifiedAt == nil || !item.VerifiedAt.Equal(verifiedAt) {
		t.Errorf("VerifiedAt = %v, want %v", item.VerifiedAt, verifiedAt)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Durable approval: a partial-batch failure leaves the remaining items in `approved` and they retry automatically without re-approval (F-52, AC-18)"
func TestReleaseStaleClaimsFreesAnAbandonedClaim(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	id := stageAndApprove(t, db, ctx, "msg-1", "hash-1")

	claimed, err := db.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("ClaimApproved: %v", err)
	}
	if claimed == nil {
		t.Fatal("ClaimApproved returned nil, want the item")
	}

	// Simulate a process that claimed the item and then died before ever
	// recording an outcome: the claim sits there, and nothing re-claims it.
	stuck, err := db.ClaimApprovedDue(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClaimApprovedDue while still claimed: %v", err)
	}
	if stuck != nil {
		t.Fatal("ClaimApprovedDue claimed an already-claimed item, want nil")
	}

	n, err := db.ReleaseStaleClaims(ctx, time.Now().UTC(), 0)
	if err != nil {
		t.Fatalf("ReleaseStaleClaims: %v", err)
	}
	if n != 1 {
		t.Errorf("ReleaseStaleClaims released %d claims, want 1", n)
	}

	recovered, err := db.ClaimApprovedDue(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClaimApprovedDue after release: %v", err)
	}
	if recovered == nil || recovered.ID != id {
		t.Fatalf("ClaimApprovedDue after ReleaseStaleClaims = %+v, want item %d claimable again with no re-approval", recovered, id)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 9, Criterion: "Label application (F-14, AC-19): after **all** documents extracted from a message reach terminal `uploaded`, issue exactly one `messages.modify` adding `VH&Co/submitted` and removing `INBOX`. Never modify a message with a non-terminal document."
func TestMarkMessageSubmittedStampsBothTimestampsTogether(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	stageMessage(t, db, ctx, "msg-1")

	msg, err := db.GetMessage(ctx, "msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.AllDocsUploadedAt != nil || msg.LabeledAt != nil {
		t.Fatal("a freshly staged message already has AllDocsUploadedAt/LabeledAt set, want both nil")
	}

	at := time.Now().UTC()
	if err := db.MarkMessageSubmitted(ctx, "msg-1", at); err != nil {
		t.Fatalf("MarkMessageSubmitted: %v", err)
	}

	msg, err = db.GetMessage(ctx, "msg-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.AllDocsUploadedAt == nil || !msg.AllDocsUploadedAt.Equal(at) {
		t.Errorf("AllDocsUploadedAt = %v, want %v", msg.AllDocsUploadedAt, at)
	}
	if msg.LabeledAt == nil || !msg.LabeledAt.Equal(at) {
		t.Errorf("LabeledAt = %v, want %v", msg.LabeledAt, at)
	}
}

// TestListUploadedOlderThanFiltersByStatusAndAge lives in
// list_uploaded_older_than_internal_test.go (package queue, white-box) since
// it needs to backdate uploaded_at directly — the public API has no other
// way to simulate the passage of time.
