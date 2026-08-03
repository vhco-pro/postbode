package clearfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// documentQuery is the only document read path the schema exposes (spec
// §6.1). Document (interface) contributes date/comment; the inline fragment
// on InvoiceDocument adds type/paymentState/file — nothing else exists.
const documentQuery = `query Document($id: ID!) {
  document(id: $id) {
    date
    comment
    ... on InvoiceDocument {
      type
      paymentState
      file { uuid name amountOfPages comment tags }
    }
  }
}`

// Document is the result of a document(id:) read, flattening the Document
// interface and its InvoiceDocument fragment (F-05, F-37).
type Document struct {
	Date         string `json:"date"`
	Comment      string `json:"comment"`
	Type         string `json:"type"`
	PaymentState string `json:"paymentState"`
	File         File   `json:"file"`
}

// Document reads back a document by the uuid returned from a prior
// UploadFile call — the only way to obtain an id at all (F-05). Used by the
// uploader (Phase 9) for proof-of-delivery verification (F-37).
func (c *Client) Document(ctx context.Context, id string) (*Document, error) {
	if id == "" {
		return nil, errors.New("clearfacts: Document: id is required")
	}

	raw, err := c.doJSON(ctx, documentQuery, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}

	var payload struct {
		Document *Document `json:"document"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("clearfacts: decode document response: %w", err)
	}
	return payload.Document, nil
}
