package dedup_test

import (
	"testing"

	"github.com/vhco-pro/postbode/internal/dedup"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "**L3** identity key from `(vendor_domain, invoice_number, invoice_date, total_amount)` parsed from filename, email subject and — where present — the PDF text layer; lower-confidence fallback key `(vendor_domain, year_month, total_amount)`; store `identity_key`, `identity_confidence` (`high`/`low`) and `identity_source` per item (F-32)"
func TestParseIdentityHighConfidenceShapes(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		filename string
		subject  string
		wantKey  string
	}{
		{
			name:     "invoice number, date and amount all in filename, underscore-delimited fields",
			vendor:   "ovh.com",
			filename: "factuur_2024-00123_2024-08-13_EUR123.45.pdf",
			subject:  "",
			wantKey:  "ovh.com|2024-00123|2024-08-13|123.45",
		},
		{
			name:     "invoice number and amount from subject, date from filename",
			vendor:   "acerta.be",
			filename: "bijlage-2026-08-13.pdf",
			subject:  "Factuurnummer: FAC-2026-0099 — totaal EUR 45,00",
			wantKey:  "acerta.be|FAC-2026-0099|2026-08-13|45.00",
		},
		{
			name:     "european dd-mm-yyyy date and european amount format",
			vendor:   "vendor.eu",
			filename: "invoice_INV2024-0099_13-08-2026_€1.234,56.pdf",
			subject:  "",
			wantKey:  "vendor.eu|INV2024-0099|2026-08-13|1234.56",
		},
		{
			name:     "us-style amount format, iso date, underscore-delimited fields",
			vendor:   "vendor.us",
			filename: "invoice_INV-77_2026-08-13_1,234.56EUR.pdf",
			subject:  "",
			wantKey:  "vendor.us|INV-77|2026-08-13|1234.56",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedup.ParseIdentity(tt.vendor, tt.filename, tt.subject)
			if got.Confidence != dedup.ConfidenceHigh {
				t.Fatalf("Confidence = %q, want %q (key=%q)", got.Confidence, dedup.ConfidenceHigh, got.Key)
			}
			if got.Key != tt.wantKey {
				t.Errorf("Key = %q, want %q", got.Key, tt.wantKey)
			}
			if got.Source == "" {
				t.Error("Source is empty, want a non-empty provenance string")
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "lower-confidence fallback key `(vendor_domain, year_month, total_amount)`; store `identity_key`, `identity_confidence` (`high`/`low`) and `identity_source` per item (F-32)"
func TestParseIdentityLowConfidenceFallback(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		filename string
		subject  string
		wantKey  string
	}{
		{
			name:     "amount present but no invoice number — falls back to year-month",
			vendor:   "ovh.com",
			filename: "statement-2026-08.pdf",
			subject:  "Your invoice — total EUR 99,00",
			wantKey:  "ovh.com|2026-08|99.00",
		},
		{
			name:     "full date present but no invoice number derives year-month from the date",
			vendor:   "acerta.be",
			filename: "bijlage-2026-08-13.pdf",
			subject:  "totaal € 45,00",
			wantKey:  "acerta.be|2026-08|45.00",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedup.ParseIdentity(tt.vendor, tt.filename, tt.subject)
			if got.Confidence != dedup.ConfidenceLow {
				t.Fatalf("Confidence = %q, want %q (key=%q)", got.Confidence, dedup.ConfidenceLow, got.Key)
			}
			if got.Key != tt.wantKey {
				t.Errorf("Key = %q, want %q", got.Key, tt.wantKey)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "Ship filename + subject parsing first, then decide the PDF-text dependency (OQ-P4) as a separate task."
//
// This is the regression guard the plan calls for explicitly: "A wrong
// high-confidence key is worse than no key." Every shape below must
// degrade to an empty Identity rather than a guessed one.
func TestParseIdentityDegradesToNoKeyRatherThanGuessing(t *testing.T) {
	tests := []struct {
		name     string
		vendor   string
		filename string
		subject  string
	}{
		{"empty everything", "ovh.com", "", ""},
		{"no vendor domain at all", "", "factuur-2024-00123-2024-08-13-EUR123.45.pdf", ""},
		{"plain filename, no invoice keyword, no amount", "ovh.com", "document.pdf", "Please see attached"},
		{"newsletter-style subject with numbers but no invoice context", "newsletter.example.com", "digest.pdf", "Your weekly digest #482, 12 new items"},
		{"invoice keyword but number has no digit", "ovh.com", "factuur-ABC.pdf", ""},
		{"amount present but no year/month or invoice number anywhere", "ovh.com", "receipt.pdf", "Thanks for your payment of EUR 45,00"},
		{"invoice number and date present but no amount anywhere", "ovh.com", "factuur_2024-00123_2024-08-13.pdf", ""},
		{"garbage date (month 13) is rejected, not guessed", "ovh.com", "factuur_2024-00123_2024-13-40_EUR10,00.pdf", ""},
		{"non-numeric currency token is rejected", "ovh.com", "invoice.pdf", "Factuur FAC-1 total EUR abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedup.ParseIdentity(tt.vendor, tt.filename, tt.subject)
			if got.Key != "" || got.Confidence != "" {
				t.Errorf("ParseIdentity(%q, %q, %q) = %+v, want the zero Identity", tt.vendor, tt.filename, tt.subject, got)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "**L3** identity key from `(vendor_domain, invoice_number, invoice_date, total_amount)` parsed from filename, email subject ... Parse European number formats too (`1.234,56` as well as `1,234.56`)."
func TestNormalizeAmountViaParseIdentity(t *testing.T) {
	tests := []struct {
		name   string
		amount string
		want   string
	}{
		{"european thousands+decimal comma", "1.234,56", "1234.56"},
		{"us thousands+decimal dot", "1,234.56", "1234.56"},
		{"bare decimal comma, no thousands", "45,00", "45.00"},
		{"bare decimal dot, no thousands", "45.00", "45.00"},
		{"single digit", "5", "5.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subject := "Factuurnummer: FAC-1 datum 2026-08-13 totaal EUR " + tt.amount
			got := dedup.ParseIdentity("ovh.com", "", subject)
			if got.Confidence != dedup.ConfidenceHigh {
				t.Fatalf("Confidence = %q, want high (key=%q)", got.Confidence, got.Key)
			}
			wantKey := "ovh.com|FAC-1|2026-08-13|" + tt.want
			if got.Key != wantKey {
				t.Errorf("Key = %q, want %q", got.Key, wantKey)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "**L4** pre-flagging: future items whose `vendor_domain` matches a vendor previously marked `already_in_portal` stage pre-flagged `probably_already_handled` (F-35, AC-13)"
func TestVendorDomain(t *testing.T) {
	tests := []struct {
		name string
		from string
		want string
	}{
		{"bare address", "billing@ovh.com", "ovh.com"},
		{"display name form", "Billing <billing@ovh.com>", "ovh.com"},
		{"upper case normalized", "Billing <BILLING@OVH.COM>", "ovh.com"},
		{"no at sign", "not-an-email", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dedup.VendorDomain(tt.from); got != tt.want {
				t.Errorf("VendorDomain(%q) = %q, want %q", tt.from, got, tt.want)
			}
		})
	}
}
