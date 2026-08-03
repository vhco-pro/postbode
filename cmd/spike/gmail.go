// DELETE AFTER P1 — see main.go and plan Phase 3/15 (F-07).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
)

// runGmailLeg is round-trip (d) (F-03, F-15, AC-4): OAuth desktop consent
// (or a cached, refreshed token), list the 5 newest Gmail message ids, and
// resolve `VH&Co/submitted` by exact name — never creating it. This is
// deliberately a thin wrapper: all the logic lives in internal/gmailwatch
// (auth.go, labels.go) so that deleting cmd/spike in Phase 15 removes no
// production code.
func runGmailLeg(ctx context.Context, credentialsPath, tokenCachePath string, out io.Writer) legResult {
	const name = "(d) gmail auth + label resolve"

	cfg, err := gmailwatch.LoadOAuthConfig(credentialsPath)
	if err != nil {
		return legResult{Name: name, Status: legFailed, Detail: err.Error()}
	}

	tok, err := gmailwatch.AuthenticateInteractive(ctx, cfg, tokenCachePath)
	if err != nil {
		return legResult{Name: name, Status: legFailed, Detail: fmt.Sprintf("authenticate: %v", err)}
	}

	svc, err := gmailwatch.NewService(ctx, cfg, tok)
	if err != nil {
		return legResult{Name: name, Status: legFailed, Detail: err.Error()}
	}

	msgResp, err := svc.Users.Messages.List("me").MaxResults(5).Context(ctx).Do()
	if err != nil {
		return legResult{Name: name, Status: legFailed, Detail: fmt.Sprintf("list messages: %v", err)}
	}
	fmt.Fprintln(out, "5 newest Gmail message ids:")
	for _, m := range msgResp.Messages {
		fmt.Fprintf(out, "  %s\n", m.Id)
	}

	label, err := gmailwatch.ResolveLabel(ctx, svc, "me", gmailwatch.SubmittedLabelName)
	if err != nil {
		if errors.Is(err, gmailwatch.ErrLabelNotFound) {
			fmt.Fprintln(out, "label not found, refusing to create")
			return legResult{Name: name, Status: legFailed, Detail: fmt.Sprintf("label %q not found, refusing to create (F-15)", gmailwatch.SubmittedLabelName)}
		}
		// Per F-15 v1.3 / OQ-P7: an auth or network failure resolving the
		// label is NOT "absent" and must not print the refusal message.
		return legResult{Name: name, Status: legFailed, Detail: fmt.Sprintf("cannot resolve label (not treated as absent, F-15 v1.3): %v", err)}
	}
	fmt.Fprintf(out, "resolved label %q id=%s\n", gmailwatch.SubmittedLabelName, label.Id)

	return legResult{Name: name, Status: legPassed}
}
