package extract

import "bytes"

// IsEncryptedPDF reports whether data carries the PDF /Encrypt trailer
// key, the stdlib-only signal for a password-protected or otherwise
// undecryptable PDF (F-22). Per NF-13, no PDF library is needed or
// permitted for this check — it is a plain byte scan, not a structural
// PDF parse, so it never panics regardless of how malformed data is
// (NF-06), including on zero-byte input.
func IsEncryptedPDF(data []byte) bool {
	return bytes.Contains(data, []byte("/Encrypt"))
}
