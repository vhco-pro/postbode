package extract

// White-box (package extract, not extract_test) tests for the unexported
// MIME walk internals — walkMIME, decodeBody, leafFilename — that the
// public ExtractMessage tests exercise indirectly but which are easier to
// pin down precisely here. Every other test file in this package uses the
// public API only.

import (
	"strings"
	"testing"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Full MIME-tree walk collecting `application/pdf` parts including nesting inside `multipart/mixed` and `multipart/related`, plus `application/octet-stream` parts whose filename ends `.pdf` (F-20)"
func TestWalkMIMECollectsOnlyPDFCandidates(t *testing.T) {
	raw := []byte("From: a@example.com\r\n" +
		"Content-Type: multipart/mixed; boundary=\"X\"\r\n\r\n" +
		"--X\r\nContent-Type: text/plain\r\n\r\nnot a candidate\r\n" +
		"--X\r\nContent-Type: application/pdf; name=\"a.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0=\r\n" +
		"--X\r\nContent-Type: application/octet-stream; name=\"b.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n" +
		"--X\r\nContent-Type: application/octet-stream; name=\"b.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n" + // NOT a candidate: octet-stream but no .pdf filename
		"--X--\r\n")

	candidates, warnings := walkMIME(raw)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2 (application/pdf + octet-stream named .pdf, excluding text/plain and octet-stream named .txt)", len(candidates))
	}

	names := map[string]bool{}
	for _, c := range candidates {
		names[c.filename] = true
	}
	if !names["a.pdf"] || !names["b.pdf"] {
		t.Errorf("candidates = %v, want a.pdf and b.pdf", names)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Handle base64 and quoted-printable transfer encodings"
func TestDecodeBodyHandlesEachTransferEncoding(t *testing.T) {
	tests := []struct {
		name string
		cte  string
		body string
		want string
	}{
		{"base64", "base64", "SGVsbG8sIFdvcmxkIQ==", "Hello, World!"},
		{"quoted-printable", "quoted-printable", "Caf=C3=A9", "Café"},
		{"7bit explicit", "7bit", "plain ascii", "plain ascii"},
		{"empty CTE defaults to raw read", "", "plain ascii", "plain ascii"},
		{"unrecognized CTE reads raw rather than erroring", "x-unknown", "plain ascii", "plain ascii"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeBody(strings.NewReader(tt.body), tt.cte)
			if err != nil {
				t.Fatalf("decodeBody(%q, cte=%q): %v", tt.body, tt.cte, err)
			}
			if string(got) != tt.want {
				t.Errorf("decodeBody(%q, cte=%q) = %q, want %q", tt.body, tt.cte, got, tt.want)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Never crash on malformed input... bad encodings"
func TestDecodeBodyInvalidBase64ReturnsErrorNotPanic(t *testing.T) {
	_, err := decodeBody(strings.NewReader("!!! not valid base64 %%%"), "base64")
	if err == nil {
		t.Fatal("decodeBody: err = nil for invalid base64, want a non-nil error")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "RFC 2047 encoded filenames"
func TestDecodeRFC2047(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain ASCII filename passes through unchanged", "invoice.pdf", "invoice.pdf"},
		{"UTF-8 B-encoded filename decodes", "=?UTF-8?B?ZmFjdHVyZS3DqXTDqS5wZGY=?=", "facture-été.pdf"},
		{"Q-encoded filename decodes", "=?UTF-8?Q?invoice=2Epdf?=", "invoice.pdf"},
		{"malformed encoded-word falls back to the raw string, no panic", "=?UTF-8?B?not-valid-base64!!?=", "=?UTF-8?B?not-valid-base64!!?="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := decodeRFC2047(tt.in)
			if got != tt.want {
				t.Errorf("decodeRFC2047(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
