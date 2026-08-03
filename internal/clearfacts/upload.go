package clearfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// uploadFileMutation is the authoritative signature from the published
// schema (spec §6.1): companyNumber, never the deprecated vatnumber.
// invoicetype is nullable in the schema but Postbode always sends PURCHASE
// explicitly (A-12).
const uploadFileMutation = `mutation UploadFile($companyNumber: String, $filename: String!, $comment: String, $tags: [String], $invoicetype: InvoiceTypeArgument) {
  uploadFile(companyNumber: $companyNumber, filename: $filename, comment: $comment, tags: $tags, invoicetype: $invoicetype) {
    uuid
    name
    amountOfPages
    comment
    tags
  }
}`

// File is the ClearFacts File type returned by uploadFile and embedded in
// document(id:) reads. Exactly these five fields exist in the published
// schema — nothing else, no duplicate/status field (spec §6.1).
type File struct {
	UUID          string   `json:"uuid"`
	Name          string   `json:"name"`
	AmountOfPages int      `json:"amountOfPages"`
	Comment       string   `json:"comment"`
	Tags          []string `json:"tags"`
}

// UploadInput is everything needed to upload one document as invoicetype
// PURCHASE. Comment and Tags are never caller-supplied — they are always
// computed from ItemID/GmailMessageID/SHA256 by ProvenanceComment and
// ProvenanceTags, so F-56 provenance stamping cannot be forgotten by a
// caller.
type UploadInput struct {
	// CompanyNumber is sent as the companyNumber argument. Never vatnumber
	// (A-12).
	CompanyNumber string
	// Filename is sent as both the required filename argument and the
	// multipart file part's Content-Disposition filename.
	Filename string
	// ItemID, GmailMessageID and SHA256 feed the provenance comment (F-56).
	ItemID         string
	GmailMessageID string
	SHA256         string
	// File is the document content. UploadFile does not close it.
	File io.Reader
}

// UploadFile uploads one document as invoicetype PURCHASE via the
// multipart/form-data contract (parts query, variables, file) and returns
// the resulting File (F-50).
func (c *Client) UploadFile(ctx context.Context, in UploadInput) (*File, error) {
	if in.File == nil {
		return nil, errors.New("clearfacts: UploadFile: File is required")
	}
	if in.Filename == "" {
		return nil, errors.New("clearfacts: UploadFile: Filename is required")
	}

	variables := map[string]any{
		"companyNumber": in.CompanyNumber,
		"filename":      in.Filename,
		"comment":       ProvenanceComment(in.ItemID, in.GmailMessageID, in.SHA256),
		"tags":          ProvenanceTags(),
		"invoicetype":   "PURCHASE",
	}

	raw, err := c.doMultipart(ctx, uploadFileMutation, variables, "file", in.Filename, in.File)
	if err != nil {
		return nil, err
	}

	var payload struct {
		UploadFile File `json:"uploadFile"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("clearfacts: decode uploadFile response: %w", err)
	}
	return &payload.UploadFile, nil
}

// provenanceCommentSHAPrefixLen bounds how much of the SHA-256 hex digest is
// embedded in the provenance comment (F-56).
const provenanceCommentSHAPrefixLen = 12

// ProvenanceTags returns the fixed provenance tag set every Postbode upload
// carries (F-56).
func ProvenanceTags() []string { return []string{"postbode"} }

// ProvenanceComment builds the F-56 provenance comment:
// "postbode <item-id> · <gmail-message-id> · <sha256-prefix>".
func ProvenanceComment(itemID, gmailMessageID, sha256Hex string) string {
	prefix := sha256Hex
	if len(prefix) > provenanceCommentSHAPrefixLen {
		prefix = prefix[:provenanceCommentSHAPrefixLen]
	}
	return fmt.Sprintf("postbode %s · %s · %s", itemID, gmailMessageID, prefix)
}
