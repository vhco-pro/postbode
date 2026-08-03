package clearfacts

import (
	"net/http"
	"strings"
	"time"
)

// ClassifyInput is everything Classify needs to make a retry decision.
// GraphQL is the gotcha (F-51): a request can be HTTP 200 and still carry a
// top-level "errors" array, so classification must look at the payload too,
// not only the status code.
type ClassifyInput struct {
	// StatusCode is the HTTP status of the response. Zero (or any value the
	// caller sets) when TransportErr is non-nil, since there was no
	// response.
	StatusCode int
	// GraphQLErrors is the response payload's top-level "errors" array, if
	// any.
	GraphQLErrors []GraphQLError
	// TransportErr is a connection-level failure (DNS, dial, TLS, timeout)
	// where no HTTP response was received at all.
	TransportErr error
}

// Decision is the outcome of classifying one response or transport error.
// Reason is empty exactly when the call succeeded.
type Decision struct {
	// Retryable is true for 429/5xx/network failures (F-51).
	Retryable bool
	// NotifyWorthy is true for 401/403 (or an equivalently-shaped GraphQL
	// auth error) — a PAT problem the operator should be told about.
	NotifyWorthy bool
	// Reason is a short human-readable explanation. Empty means success.
	Reason string
}

// Classify implements the F-51 error classification rules:
//
//	401/403                  -> terminal, notify-worthy (PAT problem)
//	4xx except 429            -> terminal
//	429/5xx/network            -> retryable
//	200 with GraphQL errors   -> terminal (or notify-worthy if auth-shaped),
//	                             never treated as a silent success
//
// This function makes a decision only; persisting retry_count and scheduling
// the next attempt is Phase 9's job (uploader). See Backoff / ShouldGiveUp
// for the pure backoff schedule that accompanies this decision.
func Classify(in ClassifyInput) Decision {
	if in.TransportErr != nil {
		return Decision{Retryable: true, Reason: "network error: " + in.TransportErr.Error()}
	}

	switch {
	case in.StatusCode == http.StatusUnauthorized || in.StatusCode == http.StatusForbidden:
		return Decision{Retryable: false, NotifyWorthy: true, Reason: "PAT invalid or missing required scope"}
	case in.StatusCode == http.StatusTooManyRequests:
		return Decision{Retryable: true, Reason: "rate limited (429)"}
	case in.StatusCode >= 500:
		return Decision{Retryable: true, Reason: fmtServerError(in.StatusCode)}
	case in.StatusCode >= 400:
		return Decision{Retryable: false, Reason: fmtClientError(in.StatusCode)}
	}

	// HTTP-level success. Still have to look at the payload (F-51 gotcha).
	if len(in.GraphQLErrors) > 0 {
		if isAuthGraphQLError(in.GraphQLErrors) {
			return Decision{Retryable: false, NotifyWorthy: true, Reason: "PAT invalid or missing required scope (GraphQL error)"}
		}
		return Decision{Retryable: false, Reason: "GraphQL error: " + in.GraphQLErrors[0].Message}
	}

	return Decision{} // success: Reason == "" is the success marker.
}

func fmtServerError(status int) string {
	return "server error (" + http.StatusText(status) + ")"
}

func fmtClientError(status int) string {
	return "client error (" + http.StatusText(status) + ")"
}

func isAuthGraphQLError(errs []GraphQLError) bool {
	for _, e := range errs {
		code, _ := e.Extensions["code"].(string)
		upperCode := strings.ToUpper(code)
		upperMsg := strings.ToUpper(e.Message)
		if strings.Contains(upperCode, "UNAUTHENT") ||
			strings.Contains(upperCode, "FORBIDDEN") ||
			strings.Contains(upperMsg, "UNAUTHORIZED") ||
			strings.Contains(upperMsg, "FORBIDDEN") ||
			strings.Contains(upperMsg, "PAT") {
			return true
		}
	}
	return false
}

// Backoff schedule constants (F-51): exponential from 1m, capped at 2h, give
// up after 24h of continuous failure.
const (
	BackoffBase        = time.Minute
	BackoffCap         = 2 * time.Hour
	BackoffGiveUpAfter = 24 * time.Hour
)

// Backoff returns the delay before retry number attempt (1-indexed): 1m, 2m,
// 4m, ... capped at 2h. It is a pure function of the attempt number only —
// persisting next_retry_at is Phase 9's job.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 30 {
		// Well past the point the doubling would overflow or exceed the
		// cap; clamp the shift to keep this a total function.
		attempt = 30
	}
	d := BackoffBase * time.Duration(uint64(1)<<uint(attempt-1))
	if d <= 0 || d > BackoffCap {
		d = BackoffCap
	}
	return d
}

// ShouldGiveUp reports whether elapsedSinceFirstFailure has crossed the
// F-51 give-up threshold (24h), at which point the item should move to
// status failed with the last error recorded (Phase 9's concern).
func ShouldGiveUp(elapsedSinceFirstFailure time.Duration) bool {
	return elapsedSinceFirstFailure >= BackoffGiveUpAfter
}
