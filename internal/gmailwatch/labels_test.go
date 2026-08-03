package gmailwatch_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
)

func newFakeGmailService(t *testing.T, handler http.HandlerFunc) *gmail.Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	svc, err := gmail.NewService(context.Background(),
		option.WithEndpoint(srv.URL),
		option.WithHTTPClient(http.DefaultClient),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("gmail.NewService: %v", err)
	}
	return svc
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "resolve `vh&co/submitted` by **exact full name**, never create; on absence exit non-zero with "label not found, refusing to create""
func TestResolveLabelReturnsLabelWhenPresent(t *testing.T) {
	svc := newFakeGmailService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gmail.ListLabelsResponse{
			Labels: []*gmail.Label{
				{Id: "Label_1", Name: "INBOX"},
				{Id: "Label_42", Name: gmailwatch.SubmittedLabelName},
			},
		})
	})

	got, err := gmailwatch.ResolveLabel(context.Background(), svc, "me", gmailwatch.SubmittedLabelName)
	if err != nil {
		t.Fatalf("ResolveLabel: %v", err)
	}
	if got.Id != "Label_42" {
		t.Errorf("ResolveLabel() id = %q, want %q", got.Id, "Label_42")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "resolve `vh&co/submitted` by **exact full name**, never create; on absence exit non-zero with "label not found, refusing to create""
func TestResolveLabelNotFoundWhenAbsentFromAuthenticatedList(t *testing.T) {
	svc := newFakeGmailService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gmail.ListLabelsResponse{
			Labels: []*gmail.Label{
				{Id: "Label_1", Name: "INBOX"},
				{Id: "Label_2", Name: "vh&co/other"}, // similarly-named, not exact
			},
		})
	})

	_, err := gmailwatch.ResolveLabel(context.Background(), svc, "me", gmailwatch.SubmittedLabelName)
	if err == nil {
		t.Fatal("ResolveLabel: want an error when the label is absent, got nil")
	}
	if err != gmailwatch.ErrLabelNotFound {
		t.Errorf("ResolveLabel() err = %v, want ErrLabelNotFound", err)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Distinguish "label absent" from "cannot check because re-auth is pending"" (F-15 v1.3, OQ-P7) — this rule is load-bearing for the Phase 3 spike's leg (d) too, since the spike is a thin caller over this exact function.
func TestResolveLabelAuthFailureIsNotTreatedAsAbsent(t *testing.T) {
	svc := newFakeGmailService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"message":"invalid_grant"}}`))
	})

	_, err := gmailwatch.ResolveLabel(context.Background(), svc, "me", gmailwatch.SubmittedLabelName)
	if err == nil {
		t.Fatal("ResolveLabel: want an error on an auth failure, got nil")
	}
	if err == gmailwatch.ErrLabelNotFound {
		t.Error("ResolveLabel: an auth failure must NOT be reported as ErrLabelNotFound (F-15 v1.3 / OQ-P7) — " +
			"it is a different failure mode (F-16's routine re-auth), and conflating the two would make the " +
			"hard refusal-to-create path fire spuriously on a routine event")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "Distinguish "label absent" from "cannot check because re-auth is pending"" (F-15 v1.3, OQ-P7)
func TestResolveLabelNetworkFailureIsNotTreatedAsAbsent(t *testing.T) {
	svc, err := gmail.NewService(context.Background(),
		option.WithEndpoint("http://127.0.0.1:1"), // nothing listens here
		option.WithHTTPClient(http.DefaultClient),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("gmail.NewService: %v", err)
	}

	_, err = gmailwatch.ResolveLabel(context.Background(), svc, "me", gmailwatch.SubmittedLabelName)
	if err == nil {
		t.Fatal("ResolveLabel: want an error on a network failure, got nil")
	}
	if err == gmailwatch.ErrLabelNotFound {
		t.Error("ResolveLabel: a network failure must NOT be reported as ErrLabelNotFound")
	}
}
