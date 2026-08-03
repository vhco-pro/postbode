package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Full MIME-tree walk collecting `application/pdf` parts... [and] RFC 2047 encoded filenames" (F-20)
func TestExtractMessageDecodesRFC2047EncodedFilename(t *testing.T) {
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	ctx := context.Background()

	raw := loadFixture(t, "rfc2047-filename.eml")
	res, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-rfc2047",
		From:           "factures@fournisseur.example.fr",
		Raw:            raw,
	})
	if err != nil {
		t.Fatalf("ExtractMessage: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(res.Items))
	}

	const want = "facture-été.pdf"
	if got := res.Items[0].OrigFilename; got != want {
		t.Errorf("OrigFilename = %q, want %q (RFC 2047 encoded-word decoded)", got, want)
	}
}
