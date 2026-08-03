package clearfacts

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "Error classification function: 401/403 → terminal `failed` + notify-worthy; 4xx except 429 → terminal `failed`; 429/5xx/network → retryable with backoff schedule 1m→2h, give up at 24h (F-51). Classification only — persistence and scheduling belong to Phase 9."
func TestClassify(t *testing.T) {
	tests := []struct {
		name              string
		in                ClassifyInput
		wantRetryable     bool
		wantNotifyWorthy  bool
		wantFailureReason bool
	}{
		{name: "401 unauthorized is terminal and notify-worthy", in: ClassifyInput{StatusCode: http.StatusUnauthorized}, wantNotifyWorthy: true, wantFailureReason: true},
		{name: "403 forbidden is terminal and notify-worthy", in: ClassifyInput{StatusCode: http.StatusForbidden}, wantNotifyWorthy: true, wantFailureReason: true},
		{name: "400 bad request is terminal, not notify-worthy", in: ClassifyInput{StatusCode: http.StatusBadRequest}, wantFailureReason: true},
		{name: "404 not found is terminal", in: ClassifyInput{StatusCode: http.StatusNotFound}, wantFailureReason: true},
		{name: "422 unprocessable is terminal", in: ClassifyInput{StatusCode: http.StatusUnprocessableEntity}, wantFailureReason: true},
		{name: "429 too many requests is retryable", in: ClassifyInput{StatusCode: http.StatusTooManyRequests}, wantRetryable: true, wantFailureReason: true},
		{name: "500 internal server error is retryable", in: ClassifyInput{StatusCode: http.StatusInternalServerError}, wantRetryable: true, wantFailureReason: true},
		{name: "503 service unavailable is retryable", in: ClassifyInput{StatusCode: http.StatusServiceUnavailable}, wantRetryable: true, wantFailureReason: true},
		{name: "network error is retryable", in: ClassifyInput{TransportErr: errors.New("dial tcp: connection refused")}, wantRetryable: true, wantFailureReason: true},
		{name: "200 with no graphql errors is a success", in: ClassifyInput{StatusCode: http.StatusOK}},
		{
			name:              "200 with a graphql error is a failure, not a silent success",
			in:                ClassifyInput{StatusCode: http.StatusOK, GraphQLErrors: []GraphQLError{{Message: "something went wrong"}}},
			wantFailureReason: true,
		},
		{
			name: "200 with an auth-shaped graphql error is notify-worthy",
			in: ClassifyInput{StatusCode: http.StatusOK, GraphQLErrors: []GraphQLError{
				{Message: "Unauthorized", Extensions: map[string]any{"code": "UNAUTHENTICATED"}},
			}},
			wantNotifyWorthy: true, wantFailureReason: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.in)
			if got.Retryable != tt.wantRetryable {
				t.Errorf("Classify(%+v).Retryable = %v, want %v", tt.in, got.Retryable, tt.wantRetryable)
			}
			if got.NotifyWorthy != tt.wantNotifyWorthy {
				t.Errorf("Classify(%+v).NotifyWorthy = %v, want %v", tt.in, got.NotifyWorthy, tt.wantNotifyWorthy)
			}
			if hasReason := got.Reason != ""; hasReason != tt.wantFailureReason {
				t.Errorf("Classify(%+v).Reason = %q, want non-empty=%v", tt.in, got.Reason, tt.wantFailureReason)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "backoff schedule 1m→2h capped, give up at 24h, as a pure function of attempt number (F-51)"
func TestBackoffSchedule(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{7, 64 * time.Minute},
		{8, 2 * time.Hour},  // 128m would exceed the 2h cap
		{20, 2 * time.Hour}, // stays capped, never grows unbounded
		{0, time.Minute},    // attempt < 1 clamps to the first attempt
	}

	for _, tt := range tests {
		if got := Backoff(tt.attempt); got != tt.want {
			t.Errorf("Backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestBackoffIsMonotonicAndCapped(t *testing.T) {
	prev := time.Duration(0)
	for attempt := 1; attempt <= 15; attempt++ {
		d := Backoff(attempt)
		if d < prev {
			t.Fatalf("Backoff(%d) = %v is less than the previous attempt's %v; the schedule must never shrink", attempt, d, prev)
		}
		if d > BackoffCap {
			t.Fatalf("Backoff(%d) = %v exceeds the %v cap", attempt, d, BackoffCap)
		}
		prev = d
	}
}

func TestShouldGiveUpAt24Hours(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    bool
	}{
		{"well within window", time.Hour, false},
		{"just under 24h", 23*time.Hour + 59*time.Minute, false},
		{"exactly 24h", 24 * time.Hour, true},
		{"past 24h", 25 * time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldGiveUp(tt.elapsed); got != tt.want {
				t.Errorf("ShouldGiveUp(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}
