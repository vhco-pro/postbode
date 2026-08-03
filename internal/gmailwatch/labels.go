package gmailwatch

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/api/gmail/v1"
)

// SubmittedLabelName is the exact full name of the Gmail label Postbode
// resolves and later applies once every document extracted from a message
// has reached terminal uploaded (F-14). Nested, exact name, never created
// (D-3, F-15).
const SubmittedLabelName = "vh&co/submitted"

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
	return nil, ErrLabelNotFound
}
