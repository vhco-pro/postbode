package extract_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Never crash on malformed input. Malformed MIME, truncated parts, bad encodings, missing headers → an error or an `unsupported_type`/`needs_manual_handling` item, never a panic. Fuzz or table-test the parser against deliberately broken input."
func TestExtractMessageNeverPanicsOnMalformedInput(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{"completely empty input", []byte("")},
		{"not RFC 822 at all", []byte("this is not an email, no headers, no body")},
		{"headers with no body", []byte("From: a@example.com\r\nSubject: x\r\n\r\n")},
		{
			"multipart Content-Type but missing boundary parameter",
			[]byte("From: a@example.com\r\nContent-Type: multipart/mixed\r\n\r\nnothing usable here"),
		},
		{
			"multipart boundary declared but body never contains it",
			[]byte("From: a@example.com\r\nContent-Type: multipart/mixed; boundary=\"X\"\r\n\r\nno boundary markers at all in this body"),
		},
		{
			"declared base64 but the body is not valid base64",
			[]byte("From: a@example.com\r\nContent-Type: multipart/mixed; boundary=\"X\"\r\n\r\n--X\r\n" +
				"Content-Type: application/pdf; name=\"x.pdf\"\r\nContent-Disposition: attachment; filename=\"x.pdf\"\r\n" +
				"Content-Transfer-Encoding: base64\r\n\r\nthis is !!! not valid base64 %%%\r\n--X--\r\n"),
		},
		{
			"truncated multipart with no closing boundary",
			[]byte("From: a@example.com\r\nContent-Type: multipart/mixed; boundary=\"X\"\r\n\r\n--X\r\n" +
				"Content-Type: application/pdf; name=\"x.pdf\"\r\nContent-Disposition: attachment; filename=\"x.pdf\"\r\n" +
				"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8="),
		},
		{
			"deeply nested multipart (recursion guard exercise)",
			deeplyNestedMultipart(50),
		},
		{
			"Content-Disposition with a malformed RFC 2047 encoded-word filename",
			[]byte("From: a@example.com\r\nContent-Type: multipart/mixed; boundary=\"X\"\r\n\r\n--X\r\n" +
				"Content-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"=?UTF-8?B?not-valid-base64!!?=\"\r\n" +
				"Content-Transfer-Encoding: base64\r\n\r\nSGVsbG8=\r\n--X--\r\n"),
		},
		{
			"binary garbage as the entire raw message",
			[]byte{0x00, 0xFF, 0x10, 0x20, 0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x00, 0x01},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openTestDB(t)
			ex := extract.New(spoolDir(t), db)
			ctx := context.Background()

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ExtractMessage panicked on malformed input %q: %v", tt.name, r)
				}
			}()

			// Either outcome is acceptable — an error (message retried next
			// poll) or a Result with zero-or-more items/warnings — the only
			// forbidden outcome is a panic.
			_, _ = ex.ExtractMessage(ctx, extract.Message{
				GmailMessageID: "msg-malformed-" + tt.name,
				From:           "a@example.com",
				Raw:            tt.raw,
			})
		})
	}
}

// deeplyNestedMultipart builds a message with n levels of multipart/mixed
// nesting, each wrapping the next, with a harmless text/plain leaf at the
// bottom — used to exercise the recursion depth guard without ever
// reaching a real PDF.
func deeplyNestedMultipart(n int) []byte {
	leaf := "Content-Type: text/plain\r\n\r\nleaf\r\n"
	body := leaf
	for i := 0; i < n; i++ {
		boundary := "B" + string(rune('a'+(i%26)))
		body = "Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n--" + boundary + "\r\n" + body + "--" + boundary + "--\r\n"
	}
	return []byte("From: a@example.com\r\n" + body)
}
