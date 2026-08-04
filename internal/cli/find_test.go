// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "`postbode status --find <term>` searching by vendor, filename, subject, invoice number and amount, printing exactly one of: `uploaded (uuid, verified-at)` / `staged` / `rejected` / `already-in-portal (marked <date>)` / `unknown` (F-39, G-5, AC-16)"
package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/queue"
)

func TestFindMatchesByVendorFilenameAndSubject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, "msg-1", "facturatie@acerta.be", "Uw loonberekening")
	stageItem(t, db, "msg-1", "hash-1", "loon-2024.pdf")

	stageMessage(t, db, "msg-2", "billing@ovh.com", "Your OVH invoice")
	stageItem(t, db, "msg-2", "hash-2", "ovh-invoice.pdf")

	for _, tt := range []struct {
		name string
		term string
		want int
	}{
		{"vendor domain", "acerta", 1},
		{"filename", "ovh-invoice", 1},
		{"subject", "loonberekening", 1},
		{"no match", "totally-unknown-vendor", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			matches, err := cli.Find(ctx, db, tt.term)
			if err != nil {
				t.Fatalf("Find(%q): %v", tt.term, err)
			}
			if len(matches) != tt.want {
				t.Errorf("Find(%q) returned %d matches, want %d", tt.term, len(matches), tt.want)
			}
		})
	}
}

func TestFindEmptyTermIsAnError(t *testing.T) {
	db := openTestDB(t)
	if _, err := cli.Find(context.Background(), db, "   "); err == nil {
		t.Error("Find(\"   \") = nil error, want an error for a blank term")
	}
}

// AC-16.
func TestFindUploadedVerdictFormat(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, "msg-1", "facturatie@acerta.be", "Factuur")
	id := stageItem(t, db, "msg-1", "hash-1", "invoice.pdf")
	if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := db.MarkUploaded(ctx, id, "uuid-xyz", 1); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	verifiedAt := time.Date(2026, 8, 1, 10, 3, 0, 0, time.UTC)
	if err := db.MarkVerified(ctx, id, verifiedAt); err != nil {
		t.Fatalf("MarkVerified: %v", err)
	}

	matches, err := cli.Find(ctx, db, "acerta")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Find: got %d matches, want 1", len(matches))
	}
	if !strings.HasPrefix(matches[0].Verdict, "uploaded (uuid=uuid-xyz, verified ") {
		t.Errorf("Verdict = %q, want it to start with the AC-16 uploaded format", matches[0].Verdict)
	}
}

func TestFindUnverifiedUploadIsDistinguishable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, "msg-1", "vendor@example.com", "invoice")
	id := stageItem(t, db, "msg-1", "hash-1", "invoice.pdf")
	if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := db.MarkUploaded(ctx, id, "uuid-unverified", 1); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	// Deliberately never call MarkVerified — F-37's "uploaded but not
	// proven delivered" state.

	matches, err := cli.Find(ctx, db, "invoice.pdf")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("Find: got %d matches, want 1", len(matches))
	}
	if !strings.Contains(matches[0].Verdict, "unverified") {
		t.Errorf("Verdict = %q, want it to flag the item as unverified (F-37)", matches[0].Verdict)
	}
}
