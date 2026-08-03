package main

import (
	"bytes"
	"testing"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "Generate the test PDF in-process (a minimal valid one-page PDF is fine, stdlib byte-writing, no dependency)"
func TestGenerateTestPDFIsStructurallyValid(t *testing.T) {
	got := generateTestPDF()

	if !bytes.HasPrefix(got, []byte("%PDF-1.4")) {
		t.Error("generateTestPDF() does not start with a %PDF header")
	}
	if !bytes.HasSuffix(got, []byte("%%EOF")) {
		t.Error("generateTestPDF() does not end with the PDF end-of-file marker")
	}
	if !bytes.Contains(got, []byte("/Type /Catalog")) {
		t.Error("generateTestPDF() is missing the Catalog object")
	}
	if !bytes.Contains(got, []byte("/Type /Page")) {
		t.Error("generateTestPDF() is missing a Page object")
	}
	if !bytes.Contains(got, []byte("xref")) {
		t.Error("generateTestPDF() is missing the xref table")
	}
	if len(got) == 0 {
		t.Error("generateTestPDF() returned no bytes")
	}
}
