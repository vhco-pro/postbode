package clearfacts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "`document(id:)` read returning `{date, comment, type, paymentState, file{uuid,name,amountOfPages,comment,tags}}` (F-05, F-37)"
func TestDocumentReadsInvoiceDocumentFields(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"document": {
					"date": "2026-08-01",
					"comment": "postbode item-1 · gmail-1 · abcdef012345",
					"type": "INVOICE",
					"paymentState": "OPEN",
					"file": {
						"uuid": "uuid-123",
						"name": "invoice.pdf",
						"amountOfPages": 3,
						"comment": "postbode item-1 · gmail-1 · abcdef012345",
						"tags": ["postbode"]
					}
				}
			}
		}`))
	}))
	defer srv.Close()

	client := clearfacts.NewClient(
		staticToken("pat"),
		clearfacts.WithEndpoint(srv.URL),
		clearfacts.WithMinInterval(0),
	)

	doc, err := client.Document(context.Background(), "uuid-123")
	if err != nil {
		t.Fatalf("Document: %v", err)
	}
	if doc.Date != "2026-08-01" || doc.Type != "INVOICE" || doc.PaymentState != "OPEN" {
		t.Errorf("Document() = %+v, unexpected core fields", doc)
	}
	if doc.File.UUID != "uuid-123" || doc.File.AmountOfPages != 3 {
		t.Errorf("Document().File = %+v, unexpected file fields", doc.File)
	}
	if len(doc.File.Tags) != 1 || doc.File.Tags[0] != "postbode" {
		t.Errorf("Document().File.Tags = %v, want [postbode]", doc.File.Tags)
	}

	query, _ := gotBody["query"].(string)
	if query == "" {
		t.Fatal("request body missing the query field")
	}
	for _, want := range []string{"... on InvoiceDocument", "paymentState", "type", "file"} {
		if !strings.Contains(query, want) {
			t.Errorf("document query %q missing %q — the inline fragment on InvoiceDocument is required to read type/paymentState", query, want)
		}
	}
}

func TestDocumentRequiresID(t *testing.T) {
	client := clearfacts.NewClient(staticToken("pat"))
	if _, err := client.Document(context.Background(), ""); err == nil {
		t.Error("Document(\"\") should error, not silently query with an empty id")
	}
}
