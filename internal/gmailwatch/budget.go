package gmailwatch

import (
	"context"
	"errors"
)

// consumesBudget reports whether err counts against a message's F-70
// consecutive-failure budget — that is, whether it is the kind of failure
// that should eventually park the message and let the poll move on.
//
// Three error shapes are excluded, each for a different reason, and each
// keeps its own existing handling untouched:
//
//   - A re-auth condition (isReauthError, F-16) short-circuits to
//     handleReauth before this is ever consulted. It is not a property of
//     the message at all: every message would fail identically, so charging
//     one of them for it would park a healthy message the moment a token
//     expired.
//
//   - A 404 / message gone (isMessageGone, c92fdb8) is skipped immediately
//     and never parked. Parking a dead id would leave it in the parked list
//     forever, with no retry that could ever succeed and nothing a human
//     could do about it. That is pure noise, and noise is precisely how a
//     report like `postbode status`'s parked section stops being read —
//     which would cost us the visibility the whole feature exists to buy.
//     isMessageGone's own doc comment draws this line and stays
//     authoritative.
//
//   - Context cancellation is the daemon shutting down mid-poll. Burning a
//     message's budget because the user quit, or because the machine went
//     to sleep, would park perfectly healthy messages — three restarts and
//     an innocent invoice is parked.
//
// Everything else consumes budget: a 500, a 429, a 403, a spool write
// failure, a malformed MIME part, a decision_log write failure. The
// classifier is deliberately a closed set of exclusions rather than a list
// of inclusions, so an error shape nobody anticipated defaults to the
// bounded, loud, recoverable path instead of to an unbounded stall.
//
// Note this does NOT absorb isMessageGone or isReauthError: they remain
// separate, separately documented predicates with their own call sites, and
// this one calls them. Collapsing the three into one function would make it
// impossible to change one policy without re-reasoning about the other two.
func consumesBudget(err error) bool {
	if err == nil {
		return false
	}
	if _, isReauth := isReauthError(err); isReauth {
		return false
	}
	if isMessageGone(err) {
		return false
	}
	// errors.Is walks the wrap chain: every call site wraps with
	// fmt.Errorf("...: %w", err), so a bare == comparison would miss.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}
