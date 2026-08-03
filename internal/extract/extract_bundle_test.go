package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Each extracted document becomes exactly one queue item. A message yielding N PDFs yields N items, all linked to the one `gmail_message_id`. (F-21, AC-6)" — exercised here at N=20 per spec §8's "email with 20 attachments (statement bundles)" edge case.
func TestExtractMessageTwentyAttachmentBundleYieldsTwentyItems(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ctx := context.Background()

	raw := loadFixture(t, "twenty-attachment-bundle.eml")
	res, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-twenty-bundle",
		From:           "statements@bank.example.com",
		Raw:            raw,
	})
	if err != nil {
		t.Fatalf("ExtractMessage: %v", err)
	}
	if res.Skipped {
		t.Fatalf("unexpectedly skipped: %s", res.SkipReason)
	}
	if len(res.Items) != 20 {
		t.Fatalf("len(Items) = %d, want 20", len(res.Items))
	}

	items, err := db.ItemsByMessageID(ctx, "msg-twenty-bundle")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 20 {
		t.Fatalf("queue rows = %d, want 20", len(items))
	}

	seenSHA := map[string]bool{}
	for _, it := range items {
		if it.GmailMessageID != "msg-twenty-bundle" {
			t.Errorf("item %d linked to %q, want msg-twenty-bundle", it.ID, it.GmailMessageID)
		}
		if seenSHA[it.SHA256] {
			t.Errorf("duplicate sha256 %s among the 20 supposedly-distinct statements", it.SHA256)
		}
		seenSHA[it.SHA256] = true
	}
}
