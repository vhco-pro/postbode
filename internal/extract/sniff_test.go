package extract_test

import (
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "MIME sniff against the ClearFacts accepted list (`application/pdf`, `image/jpeg`, `application/xml`); anything else stages `unsupported_type` with the sniffed type shown, not uploaded. In P1 only `application/pdf` reaches upload (F-25)"
func TestSniffMIME(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected string
	}{
		{"empty input", []byte(""), ""},
		{"valid PDF magic", []byte("%PDF-1.4\n..."), "application/pdf"},
		{"JPEG magic bytes", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10}, "image/jpeg"},
		{"XML declaration", []byte("<?xml version=\"1.0\"?><root/>"), "application/xml"},
		{"bare XML-ish tag without declaration", []byte("<Invoice><Total>1</Total></Invoice>"), "application/xml"},
		{"plain text", []byte("Hello, this is plain text pretending to be a PDF."), ""},
		{"too short to be any magic", []byte("PD"), ""},
		{"null bytes / garbage", []byte{0x00, 0x01, 0x02, 0x03}, ""},
		{"PDF magic requires the trailing dash", []byte("%PDF"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extract.SniffMIME(tt.data)
			if got != tt.expected {
				t.Errorf("SniffMIME(%q) = %q, want %q", tt.data, got, tt.expected)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "MIME sniff against the ClearFacts accepted list (`application/pdf`, `image/jpeg`, `application/xml`)"
func TestAcceptedMIMETypesIsExactlyTheClearFactsList(t *testing.T) {
	want := map[string]bool{
		"application/pdf": true,
		"image/jpeg":      true,
		"application/xml": true,
	}
	if len(extract.AcceptedMIMETypes) != len(want) {
		t.Fatalf("AcceptedMIMETypes has %d entries, want %d", len(extract.AcceptedMIMETypes), len(want))
	}
	for k := range want {
		if !extract.AcceptedMIMETypes[k] {
			t.Errorf("AcceptedMIMETypes missing %q", k)
		}
	}
}
