package dedup

import (
	"path"
	"strings"
)

// MatchesKnownPeppol reports whether senderAddress (a bare address or a
// full RFC 5322 "From" header value) matches any of the
// vendors.known_peppol glob patterns (F-36), e.g. "*@acerta.be". Matching
// uses path.Match — the same glob engine internal/config already
// validates rule "from" patterns against (config.go's decodeMatcher),
// keeping glob semantics identical across the codebase — against the
// lower-cased bare email address extracted from senderAddress. A glob
// that fails to compile is skipped rather than erroring (config.Load
// already rejects an invalid glob at startup, F-29; by the time this runs
// every pattern is known-good, and a defensive skip here is strictly
// safer than a panic on a malformed pattern reaching this far).
//
// This match feeds queue.NewItem.SuppressedPeppol, which still stages the
// item (status suppressed_peppol) — it is config-declared by the human
// operator, not a heuristic guess, so unlike L3/L4 it is allowed to block
// upload by default. It still never drops the item (F-36, AC-14).
func MatchesKnownPeppol(senderAddress string, globs []string) bool {
	addr := EmailAddress(senderAddress)
	if addr == "" {
		return false
	}
	for _, g := range globs {
		ok, err := path.Match(strings.ToLower(g), addr)
		if err != nil {
			continue
		}
		if ok {
			return true
		}
	}
	return false
}
