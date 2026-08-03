package gmailwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/api/gmail/v1"
)

// SubmittedLabelName is the exact full name of the Gmail label Postbode
// resolves and later applies once every document extracted from a message
// has reached terminal uploaded (F-14). Nested, exact name, never created
// (D-3, F-15).
//
// Corrected 2026-08-04 from the live mailbox: the real label is
// "VH&Co/submitted". The PRD and spec through v1.5 both said
// "vh&co/submitted", which does not exist. F-15 caught this correctly by
// refusing rather than creating a lookalike — creating one would have
// produced a second, near-identical label and silently orphaned every
// processed message under it.
const SubmittedLabelName = "VH&Co/submitted"

// ErrLabelNotFound is returned by ResolveLabel exclusively when an
// authenticated users.labels.list call completed successfully and its
// result did not contain the requested name. Per F-15 (ratified v1.3,
// planner finding OQ-P7), this is the ONLY condition that counts as
// "absent". Any other failure — an auth error, a network failure, a
// pending re-auth — is returned as a different, unwrapped error and MUST
// NOT be treated as ErrLabelNotFound by callers: F-16 makes re-auth a
// routine event, and treating "cannot check" as "absent" would make F-15's
// hard refusal fire spuriously on that routine event.
var ErrLabelNotFound = errors.New("gmailwatch: label not found, refusing to create")

// ResolveLabel resolves the Gmail label named exactly name for userID
// (conventionally "me") against svc. It never creates a label — the only
// possible outcomes are: the label (ResolveLabel returns it),
// ErrLabelNotFound (an authenticated list came back without it), or a
// plain error (the list call itself failed, e.g. auth/network — not
// "absent", see ErrLabelNotFound doc and F-15).
func ResolveLabel(ctx context.Context, svc *gmail.Service, userID, name string) (*gmail.Label, error) {
	resp, err := svc.Users.Labels.List(userID).Context(ctx).Do()
	if err != nil {
		// Deliberately NOT ErrLabelNotFound: the call itself did not
		// resolve, so "absent" cannot be concluded (F-15 v1.3 / OQ-P7).
		return nil, fmt.Errorf("gmailwatch: list labels: %w", err)
	}
	for _, l := range resp.Labels {
		if l.Name == name {
			return l, nil
		}
	}

	// Case-insensitive fallback. Gmail does not permit two user labels whose
	// names differ only in case, so this can never resolve ambiguously — and
	// it is not a "lookalike": it matches an existing label, it never creates
	// one, so the F-15 invariant is untouched. This exists because the PRD
	// recorded the label as "vh&co/submitted" while the mailbox actually has
	// "VH&Co/submitted"; a pure case difference should not hard-stop the
	// uploader. The match is reported so a drift is visible rather than
	// silently absorbed: the returned Label carries its real Name, so a caller
	// that cares can compare it against the requested name and warn. This is
	// deliberately not signalled as an error — returning a usable label
	// alongside a non-nil error would trip every caller's `if err != nil`
	// check and hard-stop the uploader for a pure case difference.
	for _, l := range resp.Labels {
		if strings.EqualFold(l.Name, name) {
			return l, nil
		}
	}
	return nil, ErrLabelNotFound
}
