package extract

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
)

// maxProposedFilenameLen is F-23's 120-char cap.
const maxProposedFilenameLen = 120

// filenameSanitizeRE matches any run of characters outside F-23's allowed
// set [A-Za-z0-9._-], collapsed to a single "-".
var filenameSanitizeRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ProposedFilename builds the F-23 proposed filename
// "{vendor}-{date}-{orig}.pdf": vendor derived from the sender's domain,
// date in ISO YYYY-MM-DD from the message date, sanitized to
// [A-Za-z0-9._-] and truncated to 120 chars. It is a pure function of its
// three inputs — the same (from, date, orig) always yields the same
// result, so re-extracting byte-identical input is collision-tolerant
// rather than accidentally producing a different name every time (F-23).
func ProposedFilename(from string, date time.Time, origFilename string) string {
	vendor := vendorDomain(from)
	if vendor == "" {
		vendor = "unknown-vendor"
	}

	dateStr := "0000-00-00"
	if !date.IsZero() {
		dateStr = date.UTC().Format("2006-01-02")
	}

	orig := strings.TrimSuffix(strings.TrimSuffix(origFilename, ".pdf"), ".PDF")
	orig = strings.TrimSpace(orig)
	if orig == "" {
		orig = "document"
	}

	name := fmt.Sprintf("%s-%s-%s.pdf", vendor, dateStr, orig)
	name = filenameSanitizeRE.ReplaceAllString(name, "-")
	return truncateFilename(name, maxProposedFilenameLen)
}

// truncateFilename caps name at maxLen bytes while preserving the .pdf
// suffix whenever possible, rather than lopping the extension off the end
// wholesale.
func truncateFilename(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name
	}
	const suffix = ".pdf"
	if strings.HasSuffix(name, suffix) && maxLen > len(suffix) {
		base := name[:len(name)-len(suffix)]
		keep := maxLen - len(suffix)
		if keep < len(base) {
			base = base[:keep]
		}
		return base + suffix
	}
	return name[:maxLen]
}

// vendorDomain extracts and sanitizes the sender's domain from a From
// header value, tolerating a bare address, a full "Name <addr>" form, or
// an unparseable value (returns "" rather than erroring, NF-06).
func vendorDomain(from string) string {
	email := from
	if addr, err := mail.ParseAddress(from); err == nil {
		email = addr.Address
	}
	at := strings.LastIndex(email, "@")
	if at == -1 || at == len(email)-1 {
		return ""
	}
	domain := strings.ToLower(email[at+1:])
	return filenameSanitizeRE.ReplaceAllString(domain, "-")
}
