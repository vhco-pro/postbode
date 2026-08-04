// DELETE AFTER P1 — see main.go and plan Phase 3/15 (F-07).
package main

import (
	"bytes"
	"fmt"
)

// generateTestPDF builds a minimal, structurally valid one-page PDF
// in-process, with a correct cross-reference table computed from the
// actual byte offsets written. No external file is fetched and no
// dependency is added — this is stdlib byte-writing only, per the Phase 3
// instruction to generate the test upload rather than reach outside the
// process for it.
func generateTestPDF() []byte {
	var buf bytes.Buffer
	var offsets [4]int // index 0 unused (object 0 is the free-list head)

	buf.WriteString("%PDF-1.4\n")

	offsets[1] = buf.Len()
	buf.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")

	offsets[2] = buf.Len()
	buf.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")

	offsets[3] = buf.Len()
	buf.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << >> >>\nendobj\n")

	xrefOffset := buf.Len()
	buf.WriteString("xref\n0 4\n")
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= 3; i++ {
		_, _ = fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	buf.WriteString("trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n")
	_, _ = fmt.Fprintf(&buf, "%d\n", xrefOffset)
	buf.WriteString("%%EOF")

	return buf.Bytes()
}
