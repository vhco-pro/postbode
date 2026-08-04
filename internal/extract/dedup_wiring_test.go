package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "`vendors.known_peppol` glob list: matching documents stage `suppressed_peppol` and are not uploadable without an explicit UI override action (F-36, AC-14)"
//
// This is the real pipeline entry point (extract.Extractor.ExtractMessage
// -> queue.DB.StageItem), not a queue-package unit test manufacturing the
// SuppressedPeppol input by hand — it proves Extractor.KnownPeppolGlobs
// actually reaches StageItem end to end.
func TestExtractMessageStagesKnownPeppolVendorAsSuppressed(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ex.KnownPeppolGlobs = []string{"*@acerta.be"}
	ctx := context.Background()

	raw := loadFixture(t, "nested-three-pdfs.eml")
	res, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-peppol",
		From:           "Acerta <facturatie@acerta.be>",
		Subject:        "Uw factuur - drie bijlagen",
		Raw:            raw,
	})
	if err != nil {
		t.Fatalf("ExtractMessage: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3", len(res.Items))
	}

	items, err := db.ItemsByMessageID(ctx, "msg-peppol")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	for _, it := range items {
		// AC-14's central assertion: staged, not dropped.
		if it.Status != queue.StatusSuppressedPeppol {
			t.Errorf("item %d Status = %s, want %s", it.ID, it.Status, queue.StatusSuppressedPeppol)
		}
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "`vendors.known_peppol` glob list: matching documents stage `suppressed_peppol` ... (F-36, AC-14)"
func TestExtractMessageDoesNotSuppressNonPeppolVendor(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ex.KnownPeppolGlobs = []string{"*@acerta.be"}
	ctx := context.Background()

	raw := loadFixture(t, "nested-three-pdfs.eml")
	_, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-not-peppol",
		From:           "billing@ovh.com",
		Subject:        "Uw factuur - drie bijlagen",
		Raw:            raw,
	})
	if err != nil {
		t.Fatalf("ExtractMessage: %v", err)
	}

	items, err := db.ItemsByMessageID(ctx, "msg-not-peppol")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	for _, it := range items {
		if it.Status != queue.StatusStaged {
			t.Errorf("item %d Status = %s, want staged (ovh.com is not a configured known-Peppol vendor)", it.ID, it.Status)
		}
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "An identity-key match **never auto-suppresses**: the item stages with a `possible duplicate of <item ref>` badge showing the matched item's status, uuid (if uploaded) and date (F-33, AC-12)"
//
// The real pipeline entry point, proving Extractor computes and passes
// identity keys through to StageItem's L3 match — not a queue-package
// unit test manufacturing the identity key by hand.
func TestExtractMessageFlagsPossibleDuplicateAcrossTwoDifferentEmails(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ctx := context.Background()

	// Same vendor, same subject shape carrying an invoice number/date/amount
	// that parses identically, but two entirely different messages/bytes
	// (different fixture attachment bytes — extract generates a synthetic
	// PDF per part, so the two ExtractMessage calls below never collide at
	// L2 even though the parsed identity is the same).
	subject := "Factuurnummer: FAC-2026-0099 datum 2026-08-13 totaal EUR 123,45"

	raw1 := loadFixture(t, "zero-byte-pdf.eml")
	first, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-identity-first",
		From:           "billing@ovh.com",
		Subject:        subject,
		Raw:            raw1,
	})
	if err != nil {
		t.Fatalf("ExtractMessage (first): %v", err)
	}
	if len(first.Items) != 1 {
		t.Fatalf("first: len(Items) = %d, want 1", len(first.Items))
	}
	if first.Items[0].Stage.Status != queue.StatusStaged {
		t.Fatalf("first item Status = %s, want staged", first.Items[0].Stage.Status)
	}

	raw2 := loadFixture(t, "corrupt-pdf.eml")
	second, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-identity-second",
		From:           "billing@ovh.com",
		Subject:        subject,
		Raw:            raw2,
	})
	if err != nil {
		t.Fatalf("ExtractMessage (second): %v", err)
	}
	if len(second.Items) != 1 {
		t.Fatalf("second: len(Items) = %d, want 1", len(second.Items))
	}

	// AC-12's central assertion: BOTH stage. Neither is suppressed.
	if second.Items[0].Stage.Status != queue.StatusStaged {
		t.Fatalf("second item Status = %s, want staged (L3 must never auto-suppress)", second.Items[0].Stage.Status)
	}
	if second.Items[0].Stage.DedupLayer != queue.DedupLayerL3 {
		t.Errorf("second item DedupLayer = %s, want %s", second.Items[0].Stage.DedupLayer, queue.DedupLayerL3)
	}

	secondItem, err := db.GetItem(ctx, second.Items[0].Stage.ItemID)
	if err != nil {
		t.Fatalf("GetItem (second): %v", err)
	}
	if !secondItem.PossibleDuplicate {
		t.Error("second item PossibleDuplicate = false, want true")
	}
	if secondItem.IdentityConfidence != "high" {
		t.Errorf("IdentityConfidence = %q, want %q", secondItem.IdentityConfidence, "high")
	}

	// Still fully approvable — the invariant that matters most.
	if err := db.Approve(ctx, secondItem.ID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve on a possible_duplicate item failed: %v", err)
	}
}
