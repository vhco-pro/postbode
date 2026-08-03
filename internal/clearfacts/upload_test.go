package clearfacts_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/clearfacts"
	"github.com/vhco-pro/postbode/internal/clearfacts/fake"
)

func staticToken(v string) clearfacts.TokenSourceFunc {
	return func(context.Context) (clearfacts.Token, error) { return clearfacts.Token(v), nil }
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "`uploadFile` as `multipart/form-data` with parts `query`, `variables`, `file`; always `companyNumber`, never `vatnumber`; `invoicetype: PURCHASE` sent explicitly even though the schema marks it nullable (F-50, A-12)"
func TestUploadFileSendsCorrectMultipartContract(t *testing.T) {
	srv := fake.New()
	defer srv.Close()

	client := clearfacts.NewClient(
		staticToken("test-pat"),
		clearfacts.WithEndpoint(srv.URL()),
		clearfacts.WithMinInterval(0),
	)

	file, err := client.UploadFile(context.Background(), clearfacts.UploadInput{
		CompanyNumber:  "BE0123456789",
		Filename:       "TEST-postbode-ignore.pdf",
		ItemID:         "item-1",
		GmailMessageID: "gmail-msg-1",
		SHA256:         "abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
		File:           strings.NewReader("%PDF-1.4 fake content"),
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if file.UUID == "" {
		t.Error("UploadFile: returned File has an empty uuid")
	}

	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("fake server received %d requests, want 1", len(reqs))
	}
	req := reqs[0]

	if req.Query == "" {
		t.Error("multipart request missing the query part")
	}
	if req.Variables == nil {
		t.Fatal("multipart request missing the variables part")
	}
	if req.FileFieldName != "file" {
		t.Errorf("file part field name = %q, want %q", req.FileFieldName, "file")
	}
	if req.FileName != "TEST-postbode-ignore.pdf" {
		t.Errorf("file part filename = %q, want TEST-postbode-ignore.pdf", req.FileName)
	}
	if got := req.Variables["companyNumber"]; got != "BE0123456789" {
		t.Errorf("variables.companyNumber = %v, want BE0123456789", got)
	}
	if _, present := req.Variables["vatnumber"]; present {
		t.Error("variables contains a vatnumber key — this must never be sent, it is deprecated (A-12)")
	}
	if got := req.Variables["invoicetype"]; got != "PURCHASE" {
		t.Errorf("variables.invoicetype = %v, want PURCHASE", got)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "Provenance arguments on every upload: `tags: [\"postbode\"]`, `comment: \"postbode <item-id> · <gmail-message-id> · <sha256-prefix>\"` (F-56)"
func TestUploadFileStampsProvenance(t *testing.T) {
	srv := fake.New()
	defer srv.Close()

	client := clearfacts.NewClient(
		staticToken("test-pat"),
		clearfacts.WithEndpoint(srv.URL()),
		clearfacts.WithMinInterval(0),
	)

	_, err := client.UploadFile(context.Background(), clearfacts.UploadInput{
		CompanyNumber:  "BE0123456789",
		Filename:       "invoice.pdf",
		ItemID:         "item-42",
		GmailMessageID: "gmail-msg-77",
		SHA256:         "0011223344556677",
		File:           bytes.NewReader([]byte("content")),
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	req := srv.Requests()[0]

	// `tags` MUST NOT be sent. The published schema documents it as a writable
	// [String] argument, but api.clearfacts.be returns an Internal server error
	// for any upload carrying it — isolated live on 2026-08-03 (comment-only
	// succeeds, tags-only fails). This assertion is inverted on purpose: it is
	// the regression guard that stops someone re-adding `tags` from the docs.
	if _, present := req.Variables["tags"]; present {
		t.Errorf("variables.tags = %v, want the key to be absent entirely; "+
			"sending tags makes the live API 500", req.Variables["tags"])
	}

	comment, _ := req.Variables["comment"].(string)
	for _, part := range []string{"postbode", "item-42", "gmail-msg-77", "001122334455"} {
		if !strings.Contains(comment, part) {
			t.Errorf("comment %q does not contain expected component %q", comment, part)
		}
	}
}

func TestUploadFileRequiresFileAndFilename(t *testing.T) {
	client := clearfacts.NewClient(staticToken("pat"))

	if _, err := client.UploadFile(context.Background(), clearfacts.UploadInput{Filename: "x.pdf"}); err == nil {
		t.Error("UploadFile with a nil File should error")
	}
	if _, err := client.UploadFile(context.Background(), clearfacts.UploadInput{File: strings.NewReader("x")}); err == nil {
		t.Error("UploadFile with an empty Filename should error")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "`internal/clearfacts/fake/` — an `httptest` server mimicking the multipart contract, scriptable to return 200 / 400 / a sequence of 503s / malformed GraphQL error payloads (NF-09)"
func TestUploadFileClassifiesFakeServerResponses(t *testing.T) {
	tests := []struct {
		name          string
		script        func(*fake.Server)
		wantRetryable bool
		wantNotify    bool
	}{
		{name: "400 is terminal", script: func(s *fake.Server) { s.Enqueue(fake.ScriptedResponse{StatusCode: 400}) }},
		{name: "401 is terminal and notify-worthy", script: func(s *fake.Server) { s.Enqueue(fake.ScriptedResponse{StatusCode: 401}) }, wantNotify: true},
		{name: "503 is retryable", script: func(s *fake.Server) { s.Enqueue(fake.ScriptedResponse{StatusCode: 503}) }, wantRetryable: true},
		{name: "malformed graphql errors payload does not panic and surfaces as an error", script: func(s *fake.Server) { s.EnqueueMalformedGraphQLErrors() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fake.New()
			defer srv.Close()
			tt.script(srv)

			client := clearfacts.NewClient(
				staticToken("test-pat"),
				clearfacts.WithEndpoint(srv.URL()),
				clearfacts.WithMinInterval(0),
			)

			_, err := client.UploadFile(context.Background(), clearfacts.UploadInput{
				CompanyNumber: "BE0123456789",
				Filename:      "invoice.pdf",
				File:          strings.NewReader("content"),
			})
			if err == nil {
				t.Fatal("UploadFile: want an error, got nil")
			}
			if got := clearfacts.IsRetryable(err); got != tt.wantRetryable {
				t.Errorf("IsRetryable(err) = %v, want %v (err=%v)", got, tt.wantRetryable, err)
			}
			if got := clearfacts.IsNotifyWorthy(err); got != tt.wantNotify {
				t.Errorf("IsNotifyWorthy(err) = %v, want %v (err=%v)", got, tt.wantNotify, err)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "A fake server returning 503 three times then 200 results in exactly one stored `uuid`, `retry_count == 3`, and no duplicate upload." (F-51 — retry sequencing exercised at the client/transport level here; persisting retry_count is Phase 9's concern)
func TestUploadFileRetrySequenceThreeFiveOhThreesThenSuccess(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	srv.EnqueueStatusSequence(503, 503, 503)

	client := clearfacts.NewClient(
		staticToken("test-pat"),
		clearfacts.WithEndpoint(srv.URL()),
		clearfacts.WithMinInterval(0),
	)

	var (
		lastErr error
		uuid    string
	)
	for attempt := 0; attempt < 4; attempt++ {
		file, err := client.UploadFile(context.Background(), clearfacts.UploadInput{
			CompanyNumber: "BE0123456789",
			Filename:      "invoice.pdf",
			File:          strings.NewReader("content"),
		})
		lastErr = err
		if err == nil {
			uuid = file.UUID
			break
		}
		if !clearfacts.IsRetryable(err) {
			t.Fatalf("attempt %d: got a non-retryable error %v, want a retryable 503", attempt, err)
		}
	}

	if lastErr != nil {
		t.Fatalf("upload never succeeded after retries: %v", lastErr)
	}
	if uuid == "" {
		t.Error("successful upload after retries returned an empty uuid")
	}
	if got := srv.RequestCount(); got != 4 {
		t.Errorf("fake server received %d requests, want 4 (3 failures + 1 success)", got)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 2, Criterion: "A fake server returning 400 marks the item `failed` immediately with `retry_count == 0`." (F-51 — classified terminal here; persisting status/retry_count is Phase 9's concern)
func TestUploadFile400IsTerminalNotRetried(t *testing.T) {
	srv := fake.New()
	defer srv.Close()
	srv.Enqueue(fake.ScriptedResponse{StatusCode: 400})

	client := clearfacts.NewClient(
		staticToken("test-pat"),
		clearfacts.WithEndpoint(srv.URL()),
		clearfacts.WithMinInterval(0),
	)

	_, err := client.UploadFile(context.Background(), clearfacts.UploadInput{
		CompanyNumber: "BE0123456789",
		Filename:      "invoice.pdf",
		File:          strings.NewReader("content"),
	})
	if err == nil {
		t.Fatal("UploadFile: want an error for a 400 response")
	}
	if clearfacts.IsRetryable(err) {
		t.Error("a 400 response must classify as terminal, not retryable")
	}
	if got := srv.RequestCount(); got != 1 {
		t.Errorf("fake server received %d requests, want exactly 1 — a terminal error must not be retried", got)
	}
}
