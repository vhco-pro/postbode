package dedup

import (
	"net/mail"
	"regexp"
	"strconv"
	"strings"
)

// Confidence values for Identity.Confidence (F-32).
const (
	ConfidenceHigh = "high"
	ConfidenceLow  = "low"
)

// Identity is the outcome of ParseIdentity: the L3 identity key plus the
// provenance a badge needs to explain itself. A zero-value Identity (empty
// Key) means "no identity could be derived" — queue.StageItem leaves
// identity_key/identity_confidence/identity_source unset in that case, and
// the item is never matched against anything by L3. That is a deliberate,
// safe default: see the package doc.
type Identity struct {
	// Key is "vendor_domain|invoice_number|invoice_date|total_amount" when
	// Confidence is "high", or "vendor_domain|year_month|total_amount"
	// when Confidence is "low". Empty when nothing could be derived.
	Key string
	// Confidence is ConfidenceHigh, ConfidenceLow, or "" (no key).
	Confidence string
	// Source records which input(s) each contributing field was parsed
	// from, e.g. "invoice_number:filename,invoice_date:subject,amount:filename".
	// Purely informational (F-32's "identity_source"); nothing parses this
	// back out. Deliberately extensible: a future PDF-text source adds one
	// more possible per-field origin ("pdf-text") without changing Key's
	// shape at all (see plan OQ-P4).
	Source string
}

// field is one parsed identity component plus where it came from.
type field struct {
	value  string
	origin string // "filename" or "subject"
}

func (f field) empty() bool { return f.value == "" }

// ParseIdentity derives the F-32 identity key from vendorDomain plus the
// filename and email subject of one candidate document. PDF text-layer
// parsing is explicitly deferred (plan OQ-P4: no NF-13-permitted
// dependency exists yet) — only filename and subject are consulted here.
//
// High confidence requires all four of vendor_domain, invoice_number,
// invoice_date and total_amount to be present. Failing that, a
// lower-confidence fallback key of (vendor_domain, year_month,
// total_amount) is used when both a year-month and an amount are
// available. If neither combination is satisfied, ParseIdentity returns
// the zero Identity — no key, not a guessed one (F-33's badge-only
// contract only works if what gets badged is actually trustworthy; a
// wrong high-confidence match is worse than a missed one).
func ParseIdentity(vendorDomain, filename, subject string) Identity {
	if vendorDomain == "" {
		return Identity{}
	}

	invoiceNumber := findInvoiceNumber(filename, subject)
	date := findDate(filename, subject)
	amount := findAmount(filename, subject)

	if !invoiceNumber.empty() && !date.empty() && !amount.empty() {
		return Identity{
			Key:        strings.Join([]string{vendorDomain, invoiceNumber.value, date.value, amount.value}, "|"),
			Confidence: ConfidenceHigh,
			Source:     joinSource(invoiceNumber, date, amount),
		}
	}

	var yearMonth field
	if !date.empty() {
		yearMonth = field{value: date.value[:7], origin: date.origin}
	} else {
		yearMonth = findYearMonth(filename, subject)
	}
	if !yearMonth.empty() && !amount.empty() {
		return Identity{
			Key:        strings.Join([]string{vendorDomain, yearMonth.value, amount.value}, "|"),
			Confidence: ConfidenceLow,
			Source:     joinSourceNamed([]namedField{{"year_month", yearMonth}, {"amount", amount}}),
		}
	}

	return Identity{}
}

type namedField struct {
	name string
	f    field
}

func joinSource(invoiceNumber, date, amount field) string {
	return joinSourceNamed([]namedField{
		{"invoice_number", invoiceNumber},
		{"invoice_date", date},
		{"amount", amount},
	})
}

func joinSourceNamed(fields []namedField) string {
	var parts []string
	for _, nf := range fields {
		if nf.f.empty() {
			continue
		}
		parts = append(parts, nf.name+":"+nf.f.origin)
	}
	return strings.Join(parts, ",")
}

// --- vendor domain / email address extraction -----------------------------

// EmailAddress extracts the bare address from an RFC 5322 "From" header
// value such as "Name <addr@example.com>" or a bare "addr@example.com",
// lower-cased. Returns the trimmed, lower-cased input unchanged when it
// does not parse as an RFC 5322 address (never errors, NF-06) — good
// enough for glob matching, which degrades to "no match" on garbage input
// rather than crashing.
func EmailAddress(from string) string {
	if addr, err := mail.ParseAddress(from); err == nil {
		return strings.ToLower(addr.Address)
	}
	return strings.ToLower(strings.TrimSpace(from))
}

// VendorDomain extracts the domain portion of an RFC 5322 "From" header
// value, lower-cased. Returns "" when no "@" can be found. This is the
// canonical vendor domain used to key L4 vendor-teaching matches (F-35)
// and known-Peppol glob matches (F-36) at staging time.
func VendorDomain(from string) string {
	addr := EmailAddress(from)
	at := strings.LastIndexByte(addr, '@')
	if at == -1 || at == len(addr)-1 {
		return ""
	}
	return addr[at+1:]
}

// --- invoice number ---------------------------------------------------------

// invoiceNumberRE requires a Belgian/Dutch/English invoice keyword
// immediately before the captured token, so an arbitrary number in a
// filename or subject is never mistaken for an invoice number (precision
// over recall — see the package doc). The captured group is validated
// separately to contain at least one digit.
// The captured character class deliberately excludes "_": in the
// underscore-field-separated filenames this corpus favours
// ("factuur_2024-00123_2024-08-13_EUR123.45.pdf"), underscore is a field
// boundary, not part of the invoice number itself. Without excluding it,
// a greedy capture would swallow the adjacent date/amount fields whole.
var invoiceNumberRE = regexp.MustCompile(
	`(?i)(?:factuur(?:nummer)?|rekening(?:nummer)?|fact(?:uur)?|invoice(?:\s*(?:number|no\.?|nr\.?))?|inv)[\s:#\-_.]{0,3}([A-Za-z0-9][A-Za-z0-9\-./]{1,24})`,
)

var trailingPunctRE = regexp.MustCompile(`[.,;:)\]]+$`)

func findInvoiceNumber(filename, subject string) field {
	for _, in := range []struct {
		text   string
		origin string
	}{{filename, "filename"}, {subject, "subject"}} {
		if v := extractInvoiceNumber(in.text); v != "" {
			return field{value: v, origin: in.origin}
		}
	}
	return field{}
}

func extractInvoiceNumber(text string) string {
	for _, m := range invoiceNumberRE.FindAllStringSubmatch(text, -1) {
		candidate := trailingPunctRE.ReplaceAllString(m[1], "")
		candidate = strings.Trim(candidate, "-_./")
		if candidate == "" || !containsDigit(candidate) {
			continue
		}
		return strings.ToUpper(candidate)
	}
	return ""
}

func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// --- date -------------------------------------------------------------------

// notDigitBoundary is used instead of \b throughout this file: Go's RE2
// \b is defined in terms of \w, which includes "_" — but this corpus's
// filenames use "_" precisely as a field separator between adjacent
// numeric tokens (invoice number, date, amount), immediately butted up
// against them ("factuur_2024-00123_2024-08-13_EUR123.45.pdf"). A literal
// \b would therefore fail to recognize a boundary at the very positions
// that matter most. "not preceded/followed by a digit" is what these
// patterns actually mean.
const (
	notDigitBoundary    = `(?:^|[^0-9])`
	notDigitBoundaryEnd = `(?:[^0-9]|$)`
)

// isoDateRE matches an unambiguous ISO date: YYYY-MM-DD or YYYY/MM/DD.
var isoDateRE = regexp.MustCompile(notDigitBoundary + `(20\d{2})[-/](\d{2})[-/](\d{2})` + notDigitBoundaryEnd)

// euroDateRE matches DD-MM-YYYY / DD/MM/YYYY / DD.MM.YYYY — the day-first
// convention the Belgian/Dutch corpus uses. Both separators must match
// (Go's RE2 has no backreferences), so this pattern is applied per
// separator rather than as one alternation.
var (
	euroDateSlashRE = regexp.MustCompile(notDigitBoundary + `(\d{1,2})/(\d{1,2})/(20\d{2})` + notDigitBoundaryEnd)
	euroDateDashRE  = regexp.MustCompile(notDigitBoundary + `(\d{1,2})-(\d{1,2})-(20\d{2})` + notDigitBoundaryEnd)
	euroDateDotRE   = regexp.MustCompile(notDigitBoundary + `(\d{1,2})\.(\d{1,2})\.(20\d{2})` + notDigitBoundaryEnd)
)

func findDate(filename, subject string) field {
	for _, in := range []struct {
		text   string
		origin string
	}{{filename, "filename"}, {subject, "subject"}} {
		if v := extractDate(in.text); v != "" {
			return field{value: v, origin: in.origin}
		}
	}
	return field{}
}

func extractDate(text string) string {
	for _, m := range isoDateRE.FindAllStringSubmatch(text, -1) {
		if d := validYMD(m[1], m[2], m[3]); d != "" {
			return d
		}
	}
	for _, re := range []*regexp.Regexp{euroDateSlashRE, euroDateDashRE, euroDateDotRE} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			// day, month, year (European day-first order).
			if d := validYMD(m[3], m[2], m[1]); d != "" {
				return d
			}
		}
	}
	return ""
}

// validYMD validates and zero-pads a year/month/day triple, rejecting
// anything that is not a real calendar date (month 1-12, day 1-31) rather
// than guessing — an unparseable-looking date degrades this field to
// empty, not to a wrong value.
func validYMD(y, m, d string) string {
	year, err1 := strconv.Atoi(y)
	month, err2 := strconv.Atoi(m)
	day, err3 := strconv.Atoi(d)
	if err1 != nil || err2 != nil || err3 != nil {
		return ""
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || year < 2000 || year > 2100 {
		return ""
	}
	return strconv.Itoa(year) + "-" + pad2(month) + "-" + pad2(day)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// yearMonthRE matches a standalone YYYY-MM / YYYY/MM token, used only as
// the low-confidence fallback's year_month component when no full date
// parsed.
var yearMonthRE = regexp.MustCompile(notDigitBoundary + `(20\d{2})[-/](\d{2})` + notDigitBoundaryEnd)

func findYearMonth(filename, subject string) field {
	for _, in := range []struct {
		text   string
		origin string
	}{{filename, "filename"}, {subject, "subject"}} {
		for _, m := range yearMonthRE.FindAllStringSubmatch(in.text, -1) {
			month, err := strconv.Atoi(m[2])
			if err == nil && month >= 1 && month <= 12 {
				return field{value: m[1] + "-" + pad2(month), origin: in.origin}
			}
		}
	}
	return field{}
}

// --- amount -------------------------------------------------------------

// amountRE requires an explicit currency marker (EUR or €) adjacent to
// the number, on either side. Filenames and subjects are dense with
// numbers that are not amounts (invoice numbers, dates); without a
// currency anchor, "amount" parsing would false-positive constantly, so
// this is deliberately conservative (see the package doc). This is a real
// coverage gap: invoice totals are rarely present in a filename or email
// subject at all — they normally live in the PDF body, which L3 does not
// parse yet (OQ-P4). High-confidence matches will accordingly be rarer
// than a reader might expect from F-32's requirement text alone.
var amountRE = regexp.MustCompile(`(?i)(?:eur|€)\s*([0-9][0-9.,]*[0-9]|[0-9])|([0-9][0-9.,]*[0-9]|[0-9])\s*(?:eur|€)`)

func findAmount(filename, subject string) field {
	for _, in := range []struct {
		text   string
		origin string
	}{{filename, "filename"}, {subject, "subject"}} {
		for _, m := range amountRE.FindAllStringSubmatch(in.text, -1) {
			raw := m[1]
			if raw == "" {
				raw = m[2]
			}
			if norm, ok := normalizeAmount(raw); ok {
				return field{value: norm, origin: in.origin}
			}
		}
	}
	return field{}
}

// normalizeAmount converts a raw numeric string in either European
// (1.234,56) or US (1,234.56) thousands/decimal convention into a
// canonical "1234.56" (always two decimal places), so the same amount
// written either way produces the same identity key component. Returns
// ok=false when the string does not parse as a number at all, rather than
// guessing.
func normalizeAmount(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	hasComma := strings.Contains(raw, ",")
	hasDot := strings.Contains(raw, ".")

	switch {
	case hasComma && hasDot:
		if strings.LastIndex(raw, ",") > strings.LastIndex(raw, ".") {
			// European: dot(s) are thousands separators, comma is decimal.
			raw = strings.ReplaceAll(raw, ".", "")
			raw = strings.Replace(raw, ",", ".", 1)
		} else {
			// US: comma(s) are thousands separators, dot is decimal.
			raw = strings.ReplaceAll(raw, ",", "")
		}
	case hasComma:
		parts := strings.Split(raw, ",")
		last := parts[len(parts)-1]
		if len(last) == 2 {
			raw = strings.Join(parts[:len(parts)-1], "") + "." + last
		} else {
			raw = strings.ReplaceAll(raw, ",", "")
		}
	case hasDot:
		parts := strings.Split(raw, ".")
		last := parts[len(parts)-1]
		if len(last) == 2 {
			raw = strings.Join(parts[:len(parts)-1], "") + "." + last
		} else {
			raw = strings.ReplaceAll(raw, ".", "")
		}
	}

	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", false
	}
	return strconv.FormatFloat(f, 'f', 2, 64), true
}
