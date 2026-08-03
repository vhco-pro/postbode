package extract

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
)

// maxMIMEDepth caps recursion into nested multipart parts. It is a
// defensive limit against pathological or hostile nesting, not a value
// any real mail client is expected to approach (F-20, NF-06).
const maxMIMEDepth = 32

// candidate is one MIME leaf part selected as a PDF extraction target
// (F-20): either declared application/pdf, or declared
// application/octet-stream with a filename ending .pdf.
type candidate struct {
	filename     string // decoded, RFC 2047-aware (F-20)
	declaredMIME string // Content-Type as declared by the message
	data         []byte // decoded body bytes
}

// walkMIME parses raw as an RFC 822 message and walks its full MIME tree
// collecting PDF candidates (F-20). It never panics: any part of the
// message that cannot be parsed or decoded is recorded as a warning
// rather than raised as a fatal error, so one malformed part never stops
// the rest of the walk (NF-06). A totally unparseable message (not even
// valid RFC 822 headers) returns a single fatal-to-this-message error —
// the caller stages nothing and the message is retried, exactly like a
// spool failure.
func walkMIME(raw []byte) ([]candidate, []error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, []error{fmt.Errorf("extract: parse RFC 822 message: %w", err)}
	}

	var candidates []candidate
	var warnings []error
	walkPart(textproto.MIMEHeader(msg.Header), msg.Body, &candidates, &warnings, 0)
	return candidates, warnings
}

func walkPart(header textproto.MIMEHeader, body io.Reader, out *[]candidate, warnings *[]error, depth int) {
	if depth > maxMIMEDepth {
		*warnings = append(*warnings, fmt.Errorf("extract: MIME nesting exceeded %d levels, stopped walking", maxMIMEDepth))
		return
	}

	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		// No parseable Content-Type defaults to text/plain per RFC 2045
		// §5.2 — never a PDF candidate, and not a fatal condition for the
		// rest of the walk.
		mediaType = "text/plain"
		params = map[string]string{}
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			*warnings = append(*warnings, fmt.Errorf("extract: multipart %q has no boundary parameter, part skipped", mediaType))
			return
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, perr := mr.NextPart()
			if perr == io.EOF {
				break
			}
			if perr != nil {
				*warnings = append(*warnings, fmt.Errorf("extract: reading a part of multipart %q: %w", mediaType, perr))
				break // malformed multipart stream: stop walking this container, never panic
			}
			walkPart(part.Header, part, out, warnings, depth+1)
			_ = part.Close()
		}
		return
	}

	// Leaf part: only application/pdf, or application/octet-stream whose
	// filename ends .pdf, are ever candidates (F-20). Everything else is
	// correctly not collected here — that is not a drop, it is F-20's own
	// scope; F-25's sniff step is what later re-validates what actually
	// got collected.
	filename := leafFilename(header, params)
	isDeclaredPDF := mediaType == "application/pdf"
	isOctetStreamPDFName := mediaType == "application/octet-stream" && strings.HasSuffix(strings.ToLower(filename), ".pdf")
	if !isDeclaredPDF && !isOctetStreamPDFName {
		return
	}

	data, derr := decodeBody(body, header.Get("Content-Transfer-Encoding"))
	if derr != nil {
		*warnings = append(*warnings, fmt.Errorf("extract: decoding part %q (filename %q): %w", mediaType, filename, derr))
		return
	}

	*out = append(*out, candidate{filename: filename, declaredMIME: mediaType, data: data})
}

// leafFilename resolves a leaf part's filename from Content-Disposition
// first, then the Content-Type "name" parameter, decoding RFC 2047
// encoded-words either way. Falls back to a generated name rather than an
// empty string so every candidate is always spoolable and loggable.
func leafFilename(header textproto.MIMEHeader, ctParams map[string]string) string {
	var raw string
	if cd := header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if fn := params["filename"]; fn != "" {
				raw = fn
			}
		}
	}
	if raw == "" {
		if fn := ctParams["name"]; fn != "" {
			raw = fn
		}
	}
	if raw == "" {
		return "attachment.pdf"
	}
	return decodeRFC2047(raw)
}

// decodeRFC2047 decodes an RFC 2047 encoded-word filename (F-20). A
// malformed encoded-word is not fatal: the raw string is used as-is
// rather than crashing or dropping the candidate (NF-06).
func decodeRFC2047(s string) string {
	dec := new(mime.WordDecoder)
	decoded, err := dec.DecodeHeader(s)
	if err != nil || decoded == "" {
		return s
	}
	return decoded
}

// decodeBody applies the part's Content-Transfer-Encoding. Unknown
// encodings are read raw rather than rejected outright — the later F-25
// sniff step is what actually decides whether the resulting bytes are
// usable, so failing loudly here would be a false negative on content
// that turns out to be fine.
func decodeBody(r io.Reader, cte string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, r))
		if err != nil {
			return nil, fmt.Errorf("base64 decode: %w", err)
		}
		return data, nil
	case "quoted-printable":
		data, err := io.ReadAll(quotedprintable.NewReader(r))
		if err != nil {
			return nil, fmt.Errorf("quoted-printable decode: %w", err)
		}
		return data, nil
	default: // "7bit", "8bit", "binary", "", and anything unrecognized
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		return data, nil
	}
}
