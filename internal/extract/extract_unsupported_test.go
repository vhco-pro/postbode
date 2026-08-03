package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "MIME sniff against the ClearFacts accepted list (`application/pdf`, `image/jpeg`, `application/xml`); anything else stages `unsupported_type` with the sniffed type shown, not uploaded. In P1 only `application/pdf` reaches upload (F-25)"
func TestExtractMessageUnsupportedContentStagesUnsupportedType(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{"zero-byte PDF", "zero-byte-pdf.eml"},
		{"corrupt PDF (declared application/pdf, garbage bytes)", "corrupt-pdf.eml"},
		{"non-PDF content with a .pdf extension", "non-pdf-with-pdf-extension.eml"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			ex := extract.New(spoolDir(t), db)
			ctx := context.Background()

			raw := loadFixture(t, tc.fixture)
			res, err := ex.ExtractMessage(ctx, extract.Message{
				GmailMessageID: "msg-" + tc.fixture,
				From:           "vendor@example.com",
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

			if !item.UnsupportedType {
				t.Error("UnsupportedType = false, want true")
			}
			// Never dropped: unsupported content is still staged (visible),
			// not silently discarded (G-1).
			if item.Status != queue.StatusStaged {
				t.Errorf("Status = %s, want staged (unsupported_type is a flag, not a drop)", item.Status)
			}
			// The sniffed type must be recorded, not left empty, so the
			// reason is visible in the UI/log (F-25).
			if item.MimeType == "" {
				t.Error("MimeType is empty, want the sniffed type recorded even though it is not accepted")
			}
			if item.MimeType == "application/pdf" {
				t.Errorf("MimeType sniffed as application/pdf for a fixture that should not be a valid PDF")
			}
		})
	}
}
