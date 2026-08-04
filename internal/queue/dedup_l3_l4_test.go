package queue_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "An identity-key match **never auto-suppresses**: the item stages with a `possible duplicate of <item ref>` badge showing the matched item's status, uuid (if uploaded) and date (F-33, AC-12)"
func TestL3IdentityKeyMatchStagesBothAndFlagsSecondAsPossibleDuplicate(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, ctx, "msg-first-email")
	firstRes, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-first-email",
		SpoolPath:      "/spool/first-bytes.pdf",
		OrigFilename:   "factuur-regenerated-v1.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      1024,
		SHA256:         "first-byte-sequence",
		IdentityKey:    "ovh.com|2026-00042|2026-08-13|123.45",
	})
	if err != nil {
		t.Fatalf("StageItem (first): %v", err)
	}
	if firstRes.Status != queue.StatusStaged {
		t.Fatalf("first item Status = %s, want staged", firstRes.Status)
	}

	// Different email, different bytes (different sha256), same parsed
	// identity — this is exactly what L3 exists to catch: a regenerated
	// PDF or a second copy of the same invoice arriving through a
	// different message (AC-12).
	stageMessage(t, db, ctx, "msg-second-email")
	secondRes, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-second-email",
		SpoolPath:      "/spool/second-bytes.pdf",
		OrigFilename:   "factuur-regenerated-v2.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      1088, // deliberately different size/bytes
		SHA256:         "second-different-byte-sequence",
		IdentityKey:    "ovh.com|2026-00042|2026-08-13|123.45",
	})
	if err != nil {
		t.Fatalf("StageItem (second): %v", err)
	}

	// AC-12's central assertion: BOTH items stage. Neither is suppressed.
	if secondRes.Status != queue.StatusStaged {
		t.Fatalf("second item Status = %s, want staged (L3 must never auto-suppress)", secondRes.Status)
	}
	if secondRes.DedupLayer != queue.DedupLayerL3 {
		t.Errorf("DedupLayer = %s, want %s", secondRes.DedupLayer, queue.DedupLayerL3)
	}
	if secondRes.LinkedItemID == nil || *secondRes.LinkedItemID != firstRes.ItemID {
		t.Fatalf("LinkedItemID = %v, want a pointer to %d", secondRes.LinkedItemID, firstRes.ItemID)
	}

	secondItem, err := db.GetItem(ctx, secondRes.ItemID)
	if err != nil {
		t.Fatalf("GetItem (second): %v", err)
	}
	if !secondItem.PossibleDuplicate {
		t.Error("second item PossibleDuplicate = false, want true")
	}
	if secondItem.LinkedItemID == nil || *secondItem.LinkedItemID != firstRes.ItemID {
		t.Errorf("second item LinkedItemID = %v, want a pointer to %d", secondItem.LinkedItemID, firstRes.ItemID)
	}

	firstItem, err := db.GetItem(ctx, firstRes.ItemID)
	if err != nil {
		t.Fatalf("GetItem (first): %v", err)
	}
	if firstItem.Status != queue.StatusStaged {
		t.Errorf("first item Status = %s after the second stage, want unchanged staged", firstItem.Status)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "An identity-key match **never auto-suppresses**: the item stages with a `possible duplicate of <item ref>` badge showing the matched item's status, uuid (if uploaded) and date (F-33, AC-12)"
//
// This is the plan's explicit regression guard: "Write an explicit test
// that a possible_duplicate / probably_already_handled item is still
// approvable." A possible_duplicate item is staged, and staged is the
// only status Approve accepts — nothing about L3 removes that.
func TestPossibleDuplicateItemRemainsApprovable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, ctx, "msg-1")
	_, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-1",
		SpoolPath:      "/spool/a.pdf",
		OrigFilename:   "a.pdf",
		SHA256:         "hash-a",
		IdentityKey:    "shared-identity",
	})
	if err != nil {
		t.Fatalf("StageItem (first): %v", err)
	}

	stageMessage(t, db, ctx, "msg-2")
	secondRes, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-2",
		SpoolPath:      "/spool/b.pdf",
		OrigFilename:   "b.pdf",
		SHA256:         "hash-b",
		IdentityKey:    "shared-identity",
	})
	if err != nil {
		t.Fatalf("StageItem (second): %v", err)
	}

	item, err := db.GetItem(ctx, secondRes.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !item.PossibleDuplicate {
		t.Fatal("second item is not flagged possible_duplicate — test setup is wrong")
	}
	if item.Status != queue.StatusStaged {
		t.Fatalf("second item Status = %s, want staged", item.Status)
	}

	// The proof: Approve succeeds. A possible_duplicate flag never blocks
	// the F-41 staged -> approved transition.
	if err := db.Approve(ctx, secondRes.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve on a possible_duplicate item failed: %v — L3 must NEVER auto-suppress", err)
	}
	approved, err := db.GetItem(ctx, secondRes.ItemID)
	if err != nil {
		t.Fatalf("GetItem after approve: %v", err)
	}
	if approved.Status != queue.StatusApproved {
		t.Errorf("Status after Approve = %s, want approved", approved.Status)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "**L4** pre-flagging: future items whose `vendor_domain` matches a vendor previously marked `already_in_portal` stage pre-flagged `probably_already_handled` **with the reason and the teaching date shown**, still staged, still human-decidable, never auto-rejected (F-35, AC-13)"
func TestL4VendorTeachingMatchFlagsLaterItemAsProbablyAlreadyHandled(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Teach the vendor directly (simulating the webui's "already in
	// portal" endpoint, which is the real F-34 writer — Phase 8).
	stageMessage(t, db, ctx, "msg-taught")
	taughtID := stageItem(t, db, ctx, "msg-taught", "hash-taught")
	if err := db.MarkAlreadyInPortalWithTeaching(ctx, taughtID, queue.ActorHuman, queue.VendorTeaching{
		VendorDomain: "acerta.be",
		Reason:       "marked already in portal by reviewer",
		Note:         "seen it arrive via Peppol",
	}); err != nil {
		t.Fatalf("MarkAlreadyInPortalWithTeaching: %v", err)
	}

	// A later, unrelated invoice from the same vendor (different
	// identity_key, different message, different bytes) must stage
	// pre-flagged.
	stageMessage(t, db, ctx, "msg-later")
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-later",
		SpoolPath:      "/spool/later.pdf",
		OrigFilename:   "later-invoice.pdf",
		SHA256:         "hash-later",
		IdentityKey:    "acerta.be|2026-09|999.00",
		VendorDomain:   "acerta.be",
	})
	if err != nil {
		t.Fatalf("StageItem (later): %v", err)
	}
	if res.Status != queue.StatusStaged {
		t.Fatalf("later item Status = %s, want staged (L4 must never auto-reject)", res.Status)
	}

	item, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !item.ProbablyAlreadyHandled {
		t.Error("ProbablyAlreadyHandled = false, want true")
	}

	// The reason and teaching date are recoverable by vendor domain — the
	// UI's rendering path (webui/view.go's teachingDetail).
	teaching, err := db.GetVendorTeachingByVendorDomain(ctx, "acerta.be")
	if err != nil {
		t.Fatalf("GetVendorTeachingByVendorDomain: %v", err)
	}
	if teaching.MarkedAt.IsZero() {
		t.Error("MarkedAt is zero, want set")
	}
	if teaching.Note != "seen it arrive via Peppol" {
		t.Errorf("Note = %q, want the taught note", teaching.Note)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "**L4** pre-flagging ... still staged, still human-decidable, never auto-rejected (F-35, AC-13)"
//
// The same regression guard as TestPossibleDuplicateItemRemainsApprovable,
// for L4: a probably_already_handled item must still be approvable.
func TestProbablyAlreadyHandledItemRemainsApprovable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, ctx, "msg-taught")
	taughtID := stageItem(t, db, ctx, "msg-taught", "hash-taught")
	if err := db.MarkAlreadyInPortalWithTeaching(ctx, taughtID, queue.ActorHuman, queue.VendorTeaching{
		VendorDomain: "ovh.com",
		Reason:       "marked already in portal by reviewer",
	}); err != nil {
		t.Fatalf("MarkAlreadyInPortalWithTeaching: %v", err)
	}

	stageMessage(t, db, ctx, "msg-later")
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-later",
		SpoolPath:      "/spool/later.pdf",
		OrigFilename:   "later.pdf",
		SHA256:         "hash-later",
		VendorDomain:   "ovh.com",
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}

	item, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !item.ProbablyAlreadyHandled {
		t.Fatal("item is not flagged probably_already_handled — test setup is wrong")
	}

	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve on a probably_already_handled item failed: %v — L4 must NEVER auto-reject", err)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "`vendors.known_peppol` glob list: matching documents stage `suppressed_peppol` and are not uploadable without an explicit UI override action (F-36, AC-14)"
func TestSuppressedPeppolStagesButIsNotDirectlyApprovable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, ctx, "msg-1")
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   "msg-1",
		SpoolPath:        "/spool/a.pdf",
		OrigFilename:     "a.pdf",
		SHA256:           "hash-a",
		VendorDomain:     "acerta.be",
		SuppressedPeppol: true,
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}

	// AC-14's central assertion: the item stages. It is not dropped.
	if res.Status != queue.StatusSuppressedPeppol {
		t.Fatalf("Status = %s, want %s — a known-Peppol match must still stage, never be dropped", res.Status, queue.StatusSuppressedPeppol)
	}

	// Approve is not offered without an explicit override: staged is the
	// only status Approve accepts as a starting point.
	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err == nil {
		t.Fatal("Approve succeeded directly from suppressed_peppol, want an InvalidTransitionError")
	}

	// The explicit override unblocks it, landing on the ordinary staged
	// flow — from which Approve now works normally.
	if err := db.OverridePeppolSuppression(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("OverridePeppolSuppression: %v", err)
	}
	overridden, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if overridden.Status != queue.StatusStaged {
		t.Fatalf("Status after override = %s, want staged", overridden.Status)
	}
	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve after override: %v", err)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "L3 and L4 never auto-suppress. It sets `possible_duplicate` and records the matched item so the UI can show *\"possible duplicate of #N\"* with that item's status, uuid and date. **The item stays fully approvable.**"
//
// A stronger version of the two "remains approvable" tests above: an item
// that is flagged by BOTH L3 and L4 simultaneously is still exactly as
// approvable as a plain item — the flags are purely additive metadata.
func TestItemFlaggedByBothL3AndL4IsStillFullyApprovable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, ctx, "msg-original")
	if _, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-original",
		SpoolPath:      "/spool/original.pdf",
		OrigFilename:   "original.pdf",
		SHA256:         "hash-original",
		IdentityKey:    "acerta.be|2026-00099|2026-08-13|500.00",
	}); err != nil {
		t.Fatalf("StageItem (original): %v", err)
	}

	stageMessage(t, db, ctx, "msg-taught")
	taughtID := stageItem(t, db, ctx, "msg-taught", "hash-taught")
	if err := db.MarkAlreadyInPortalWithTeaching(ctx, taughtID, queue.ActorHuman, queue.VendorTeaching{
		VendorDomain: "acerta.be",
		Reason:       "marked already in portal by reviewer",
	}); err != nil {
		t.Fatalf("MarkAlreadyInPortalWithTeaching: %v", err)
	}

	stageMessage(t, db, ctx, "msg-both-flags")
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-both-flags",
		SpoolPath:      "/spool/both.pdf",
		OrigFilename:   "both.pdf",
		SHA256:         "hash-both",
		IdentityKey:    "acerta.be|2026-00099|2026-08-13|500.00",
		VendorDomain:   "acerta.be",
	})
	if err != nil {
		t.Fatalf("StageItem (both flags): %v", err)
	}

	item, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !item.PossibleDuplicate || !item.ProbablyAlreadyHandled {
		t.Fatalf("expected both flags set, got PossibleDuplicate=%v ProbablyAlreadyHandled=%v", item.PossibleDuplicate, item.ProbablyAlreadyHandled)
	}
	if item.Status != queue.StatusStaged {
		t.Fatalf("Status = %s, want staged", item.Status)
	}

	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve on a doubly-flagged item failed: %v", err)
	}
}
