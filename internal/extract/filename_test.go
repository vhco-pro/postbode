package extract_test

import (
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Proposed filename `{vendor}-{date}-{orig}.pdf`, vendor from sender domain, date ISO `YYYY-MM-DD` from the message date, sanitized to `[A-Za-z0-9._-]`, truncated to 120 chars (F-23)"
func TestProposedFilename(t *testing.T) {
	date := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		from     string
		date     time.Time
		orig     string
		expected string
	}{
		{
			name:     "simple bare address",
			from:     "billing@ovh.com",
			date:     date,
			orig:     "invoice.pdf",
			expected: "ovh.com-2026-08-03-invoice.pdf",
		},
		{
			name:     "From header with display name",
			from:     `"OVH Billing" <billing@ovh.com>`,
			date:     date,
			orig:     "invoice.pdf",
			expected: "ovh.com-2026-08-03-invoice.pdf",
		},
		{
			name:     "original filename with spaces and unicode gets sanitized",
			from:     "factures@fournisseur.example.fr",
			date:     date,
			orig:     "facture été (1).pdf",
			expected: "fournisseur.example.fr-2026-08-03-facture-t-1-.pdf",
		},
		{
			name:     "unparseable From falls back to unknown-vendor",
			from:     "not an email address at all",
			date:     date,
			orig:     "invoice.pdf",
			expected: "unknown-vendor-2026-08-03-invoice.pdf",
		},
		{
			name:     "zero date falls back to 0000-00-00",
			from:     "billing@ovh.com",
			date:     time.Time{},
			orig:     "invoice.pdf",
			expected: "ovh.com-0000-00-00-invoice.pdf",
		},
		{
			name:     "empty original filename falls back to document",
			from:     "billing@ovh.com",
			date:     date,
			orig:     "",
			expected: "ovh.com-2026-08-03-document.pdf",
		},
		{
			name:     "uppercase .PDF suffix stripped like .pdf",
			from:     "billing@ovh.com",
			date:     date,
			orig:     "INVOICE.PDF",
			expected: "ovh.com-2026-08-03-INVOICE.pdf",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extract.ProposedFilename(tt.from, tt.date, tt.orig)
			if got != tt.expected {
				t.Errorf("ProposedFilename(%q, %v, %q) = %q, want %q", tt.from, tt.date, tt.orig, got, tt.expected)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Proposed filename `{vendor}-{date}-{orig}.pdf`, vendor from sender domain, date ISO `YYYY-MM-DD` from the message date, sanitized to `[A-Za-z0-9._-]`, truncated to 120 chars (F-23)"
func TestProposedFilenameTruncatedTo120CharsPreservingExtension(t *testing.T) {
	date := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	longOrig := strings.Repeat("a-very-long-original-filename-segment-", 5) + ".pdf"

	got := extract.ProposedFilename("billing@a-very-long-vendor-domain-name.example.com", date, longOrig)

	if len(got) > 120 {
		t.Fatalf("ProposedFilename length = %d, want <= 120: %q", len(got), got)
	}
	if !strings.HasSuffix(got, ".pdf") {
		t.Errorf("ProposedFilename = %q, want it to still end in .pdf after truncation", got)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Must be deterministic and collision-tolerant."
func TestProposedFilenameIsDeterministic(t *testing.T) {
	date := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	a := extract.ProposedFilename("billing@ovh.com", date, "invoice.pdf")
	b := extract.ProposedFilename("billing@ovh.com", date, "invoice.pdf")
	if a != b {
		t.Errorf("ProposedFilename is not deterministic: %q != %q for identical input", a, b)
	}
}
