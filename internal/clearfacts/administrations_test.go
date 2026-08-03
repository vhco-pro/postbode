package clearfacts_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "`administrations(offset, first) { companyNumber }` query (F-01)"
func TestAdministrationsReturnsCompanyNumbers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"administrations":[{"companyNumber":"BE0123456789"},{"companyNumber":"BE0999999999"}]}}`))
	}))
	defer srv.Close()

	client := clearfacts.NewClient(
		staticToken("pat"),
		clearfacts.WithEndpoint(srv.URL),
		clearfacts.WithMinInterval(0),
	)

	admins, err := client.Administrations(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("Administrations: %v", err)
	}
	if len(admins) != 2 {
		t.Fatalf("Administrations() returned %d entries, want 2", len(admins))
	}
	for _, a := range admins {
		if a.CompanyNumber == "" {
			t.Error("Administrations() returned an entry with an empty companyNumber")
		}
	}
}

func TestAdministrationsUsesAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"administrations":[]}}`))
	}))
	defer srv.Close()

	client := clearfacts.NewClient(
		staticToken("super-secret-pat"),
		clearfacts.WithEndpoint(srv.URL),
		clearfacts.WithMinInterval(0),
	)
	if _, err := client.Administrations(context.Background(), 0, 0); err != nil {
		t.Fatalf("Administrations: %v", err)
	}
	if gotAuth != "Bearer super-secret-pat" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer super-secret-pat")
	}
}
