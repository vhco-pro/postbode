package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Given a password-protected PDF fixture, the item is staged with `needs_manual_handling=true`, is not uploadable from the UI, and is never sent to the fake upload server. (F-22) — AC-7"
func TestExtractMessagePasswordProtectedPDFStagesNeedsManualHandling(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ctx := context.Background()

	raw := loadFixture(t, "password-protected.eml")
	res, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-password-protected",
		From:           "facturatie@acerta.be",
		Raw:            raw,
	})
	if err != nil {
		t.Fatalf("ExtractMessage: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(res.Items))
	}

	item, err := db.GetItem(ctx, res.Items[0].Stage.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}

	// Never dropped: it is staged like any other item.
	if item.Status != queue.StatusStaged {
		t.Errorf("Status = %s, want staged (never dropped)", item.Status)
	}
	// Flagged for manual handling — this is what the UI (Phase 8) uses to
	// disable the Approve action and what the uploader (Phase 9) must
	// never send to the fake/real upload server.
	if !item.NeedsManualHandling {
		t.Error("NeedsManualHandling = false, want true for a password-protected PDF")
	}
	// A password-protected PDF still sniffs to application/pdf by magic
	// bytes — needs_manual_handling is the signal, not unsupported_type.
	if item.UnsupportedType {
		t.Error("UnsupportedType = true, want false — the file IS a PDF, it's just encrypted")
	}
	if item.MimeType != "application/pdf" {
		t.Errorf("MimeType = %q, want application/pdf", item.MimeType)
	}
}
