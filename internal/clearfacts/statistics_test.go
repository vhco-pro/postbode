package clearfacts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "`companyStatistics(type, startPeriod, endPeriod, companyNumber, invoicetype)` call with a caller-supplied `type` string, for the Phase 3 probe (F-08)"
func TestCompanyStatisticsAcceptsCallerSuppliedType(t *testing.T) {
	tests := []struct {
		name     string
		typeArg  string
		respBody string
		wantErr  bool
	}{
		{
			name:     "a working type string returns items",
			typeArg:  "invoice_count",
			respBody: `{"data":{"companyStatistics":[{"companyNumber":"BE0123456789","items":[{"period":["2026-08"],"value":[3]}]}]}}`,
		},
		{
			name:     "a non-working type string surfaces as a classified error, not a panic",
			typeArg:  "not_a_real_type",
			respBody: `{"errors":[{"message":"invalid type argument"}]}`,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotVars map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Variables map[string]any `json:"variables"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				gotVars = body.Variables
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.respBody))
			}))
			defer srv.Close()

			client := clearfacts.NewClient(
				staticToken("pat"),
				clearfacts.WithEndpoint(srv.URL),
				clearfacts.WithMinInterval(0),
			)

			stats, err := client.CompanyStatistics(context.Background(), clearfacts.CompanyStatisticsInput{
				Type:          tt.typeArg,
				StartPeriod:   "2026-08-01",
				EndPeriod:     "2026-08-31",
				CompanyNumber: "BE0123456789",
				InvoiceType:   "PURCHASE",
			})

			if tt.wantErr {
				if err == nil {
					t.Fatal("CompanyStatistics: want an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CompanyStatistics: %v", err)
			}
			if len(stats) != 1 || len(stats[0].Items) != 1 {
				t.Fatalf("CompanyStatistics() = %+v, want one statistic with one item", stats)
			}
			if gotVars["type"] != tt.typeArg {
				t.Errorf("variables.type = %v, want %v", gotVars["type"], tt.typeArg)
			}
			if gotVars["invoicetype"] != "PURCHASE" {
				t.Errorf("variables.invoicetype = %v, want PURCHASE", gotVars["invoicetype"])
			}
		})
	}
}

func TestCompanyStatisticsRequiresType(t *testing.T) {
	client := clearfacts.NewClient(staticToken("pat"))
	_, err := client.CompanyStatistics(context.Background(), clearfacts.CompanyStatisticsInput{
		StartPeriod: "2026-08-01",
		EndPeriod:   "2026-08-31",
	})
	if err == nil {
		t.Error("CompanyStatistics with an empty Type should error — the caller must supply the candidate to probe (F-08)")
	}
}
