package extract

import "bytes"

// AcceptedMIMETypes is the ClearFacts-accepted upload MIME list (F-25,
// spec §3.3 / §6.1). In P1 only application/pdf actually reaches upload;
// image/jpeg and application/xml are recognized so P2 (images) is
// additive rather than a rewrite (spec §9).
var AcceptedMIMETypes = map[string]bool{
	"application/pdf": true,
	"image/jpeg":      true,
	"application/xml": true,
}

// SniffMIME inspects the actual bytes of a spooled candidate — never the
// declared Content-Type — and returns the ClearFacts-recognized MIME type
// it sniffs to, or "" when the content cannot be classified as one of
// application/pdf, image/jpeg or application/xml. The caller stages a ""
// result as unsupported_type (F-25). Never panics on short, empty, or
// arbitrary byte slices (NF-06).
func SniffMIME(data []byte) string {
	switch {
	case looksLikePDF(data):
		return "application/pdf"
	case looksLikeJPEG(data):
		return "image/jpeg"
	case looksLikeXML(data):
		return "application/xml"
	default:
		return ""
	}
}

func looksLikePDF(data []byte) bool {
	return len(data) >= 5 && bytes.Equal(data[:5], []byte("%PDF-"))
}

func looksLikeJPEG(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF
}

// utf8BOM is the 3-byte UTF-8 byte order mark, spelled as an escape
// sequence rather than a literal character so this source file never
// itself contains a raw BOM.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func looksLikeXML(data []byte) bool {
	trimmed := bytes.TrimPrefix(data, utf8BOM)
	trimmed = bytes.TrimLeft(trimmed, " \t\r\n")
	if len(trimmed) == 0 {
		return false
	}
	return bytes.HasPrefix(trimmed, []byte("<?xml")) || trimmed[0] == '<'
}
