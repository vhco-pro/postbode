package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Given a `testdata/*.eml` fixture with two PDFs nested in `multipart/mixed` plus one `application/octet-stream` named `*.pdf`, the extractor produces exactly 3 queue items linked to one `gmail_message_id`. (F-20, F-21) — AC-6"
func TestExtractMessageNestedThreePDFsYieldsThreeLinkedItems(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ctx := context.Background()

	raw := loadFixture(t, "nested-three-pdfs.eml")
	res, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-nested-three",
		From:           "billing@ovh.com",
		Raw:            raw,
	})
	if err != nil {
		t.Fatalf("ExtractMessage: %v", err)
	}
	if res.Skipped {
		t.Fatalf("ExtractMessage: unexpectedly skipped: %s", res.SkipReason)
	}
	if len(res.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3 (2 nested application/pdf + 1 octet-stream .pdf)", len(res.Items))
	}

	items, err := db.ItemsByMessageID(ctx, "msg-nested-three")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("queue rows for msg-nested-three = %d, want 3", len(items))
	}
	for _, it := range items {
		if it.GmailMessageID != "msg-nested-three" {
			t.Errorf("item %d GmailMessageID = %q, want msg-nested-three", it.ID, it.GmailMessageID)
		}
		if it.Status != queue.StatusStaged {
			t.Errorf("item %d Status = %s, want staged", it.ID, it.Status)
		}
		if it.MimeType != "application/pdf" {
			t.Errorf("item %d MimeType = %q, want application/pdf (sniffed, not declared)", it.ID, it.MimeType)
		}
	}

	// Distinct SHA-256s: each of the three generated PDFs differs in byte
	// content, so none of them should have collided with L2.
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.SHA256] {
			t.Errorf("duplicate sha256 %s across supposedly distinct PDFs", it.SHA256)
		}
		seen[it.SHA256] = true
	}
}
