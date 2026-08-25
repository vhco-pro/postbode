package gmailwatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "Table test in internal/gmailwatch/budget_test.go: 404, 403, 429, 500, 503, oauth2.RetrieveError{invalid_grant}, context.Canceled, context.DeadlineExceeded, a plain errors.New, a wrapped 404, a wrapped cancellation, and nil."
func TestConsumesBudget(t *testing.T) {
	apiErr := func(code int) error { return &googleapi.Error{Code: code, Message: "scripted"} }

	cases := []struct {
		name string
		err  error
		want bool
		why  string
	}{
		{"nil", nil, false, "no failure happened"},
		{"404 gone", apiErr(http.StatusNotFound), false, "the message no longer exists; parking a dead id is pure noise"},
		{"403 forbidden", apiErr(http.StatusForbidden), true, "may be a real permission failure; park loudly rather than stall"},
		{"429 rate limited", apiErr(http.StatusTooManyRequests), true, "sustained rate limiting must not stall the mailbox forever"},
		{"500 server error", apiErr(http.StatusInternalServerError), true, "the canonical budget-consuming failure"},
		{"503 unavailable", apiErr(http.StatusServiceUnavailable), true, "transient shapes still consume budget; the budget is what makes them self-heal"},
		{"oauth invalid_grant", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}, false, "re-auth is not a property of any one message"},
		{"context canceled", context.Canceled, false, "the daemon is shutting down, not the message failing"},
		{"context deadline", context.DeadlineExceeded, false, "same: a timeout on shutdown must not park healthy mail"},
		{"plain error", errors.New("spool: disk full"), true, "an unanticipated shape defaults to the bounded, loud path"},

		// Every real call site wraps, so the wrapped forms are the ones
		// that actually matter in production.
		{"wrapped 404", fmt.Errorf("gmailwatch: messages.get(x): %w", apiErr(http.StatusNotFound)), false, "wrapping must not defeat the exclusion"},
		{"wrapped cancellation", fmt.Errorf("gmailwatch: poll: %w", context.Canceled), false, "wrapping must not defeat the exclusion"},
		{"wrapped reauth", fmt.Errorf("gmailwatch: poll: %w", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}), false, "wrapping must not defeat the exclusion"},
		{"wrapped 500", fmt.Errorf("gmailwatch: extract message x: %w", apiErr(http.StatusInternalServerError)), true, "wrapping must not defeat inclusion either"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := consumesBudget(tc.err); got != tc.want {
				t.Errorf("consumesBudget(%v) = %v, want %v — %s", tc.err, got, tc.want, tc.why)
			}
		})
	}
}

// The three predicates must stay separate and stay consistent: an error
// excluded by consumesBudget for the "gone" reason must still be recognised
// as gone by the code that logs `skip (gone)`, and likewise for re-auth.
func TestConsumesBudgetAgreesWithTheSiblingPredicates(t *testing.T) {
	gone := &googleapi.Error{Code: http.StatusNotFound}
	if !isMessageGone(gone) || consumesBudget(gone) {
		t.Error("a 404 must be gone=true and budget-consuming=false")
	}

	reauth := &oauth2.RetrieveError{ErrorCode: "invalid_grant"}
	if _, ok := isReauthError(reauth); !ok || consumesBudget(reauth) {
		t.Error("an invalid_grant must be reauth=true and budget-consuming=false")
	}

	// And a plain server error must be neither, so it falls through to the
	// budget path rather than being silently swallowed by an exclusion.
	boom := &googleapi.Error{Code: http.StatusInternalServerError}
	if isMessageGone(boom) {
		t.Error("a 500 must not be classified as gone")
	}
	if _, ok := isReauthError(boom); ok {
		t.Error("a 500 must not be classified as a re-auth condition")
	}
	if !consumesBudget(boom) {
		t.Error("a 500 must consume budget")
	}
}
