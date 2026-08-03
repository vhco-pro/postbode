package extract_test

import (
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Password-protected / undecryptable PDF detection via the `/Encrypt` trailer key (stdlib byte scan, no new dependency): stage with `needs_manual_handling=true`, never drop, never upload (F-22, AC-7)"
func TestIsEncryptedPDF(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected bool
	}{
		{"empty input never panics, not encrypted", []byte{}, false},
		{"plain PDF with no /Encrypt key", []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Size 1 /Root 1 0 R >>\n%%EOF"), false},
		{"trailer carries /Encrypt", []byte("%PDF-1.4\n...trailer\n<< /Size 5 /Root 1 0 R /Encrypt 4 0 R >>\n%%EOF"), true},
		{"garbage bytes, no panic, not encrypted", []byte{0x00, 0x01, 0xFF, 0xFE, 0x10}, false},
		{"/Encrypt token appears anywhere in the byte stream", []byte("garbage /Encrypt more garbage"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extract.IsEncryptedPDF(tt.data)
			if got != tt.expected {
				t.Errorf("IsEncryptedPDF(%q) = %v, want %v", tt.data, got, tt.expected)
			}
		})
	}
}
