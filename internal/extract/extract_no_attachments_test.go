package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Turn one Gmail message into N queue items, dropping nothing and never crashing on malformed input" — N=0 for a message with no attachments at all is the boundary case: zero items is correct, not a crash and not an error.
func TestExtractMessageNoAttachmentsYieldsZeroItemsNoCrash(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ctx := context.Background()

	raw := loadFixture(t, "no-attachments.eml")
	res, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-no-attachments",
		From:           "news@newsletter.example.com",
		Raw:            raw,
	})
	if err != nil {
		t.Fatalf("ExtractMessage: %v", err)
	}
	if res.Skipped {
		t.Fatalf("unexpectedly skipped: %s", res.SkipReason)
	}
	if len(res.Items) != 0 {
		t.Fatalf("len(Items) = %d, want 0", len(res.Items))
	}

	// The message itself is still recorded (L1) even with nothing to
	// extract, so a later re-poll of this same message id does not
	// re-walk it.
	seen, err := db.MessageSeen(ctx, "msg-no-attachments")
	if err != nil {
		t.Fatalf("MessageSeen: %v", err)
	}
	if !seen {
		t.Error("MessageSeen = false after ExtractMessage, want true — a no-attachment message should still be recorded once processed")
	}
}
