package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/vhco-pro/postbode/internal/queue"
)

// FindMatch is one item whose local record matched a `postbode status
// --find <term>` search (F-39), together with the one-line G-5 verdict for
// it.
type FindMatch struct {
	Item    *queue.Item
	Message *queue.Message // nil when the message row could not be read
	Verdict string
}

// Find searches every item's vendor (message From), filename, subject and
// identity key (which carries invoice number, invoice date and amount once
// L3 populates it — Phase 12) for a case-insensitive substring match on
// term, returning one FindMatch per hit with its G-5 verdict already
// computed, newest-staged first. An empty term is a caller error, not "no
// results".
func Find(ctx context.Context, db *queue.DB, term string) ([]FindMatch, error) {
	needle := strings.ToLower(strings.TrimSpace(term))
	if needle == "" {
		return nil, errors.New("cli: Find: term must not be empty")
	}

	var out []FindMatch
	for _, st := range allStatuses {
		items, err := db.ListByStatus(ctx, st)
		if err != nil {
			return nil, fmt.Errorf("cli: Find: %w", err)
		}
		for _, it := range items {
			msg, _ := db.GetMessage(ctx, it.GmailMessageID) // best-effort; nil is fine, just fewer fields to match/show
			if !matchesTerm(needle, it, msg) {
				continue
			}
			verdict, err := verdictFor(ctx, db, it)
			if err != nil {
				return nil, fmt.Errorf("cli: Find: %w", err)
			}
			out = append(out, FindMatch{Item: it, Message: msg, Verdict: verdict})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Item.StagedAt.After(out[j].Item.StagedAt)
	})
	return out, nil
}

func matchesTerm(needle string, it *queue.Item, msg *queue.Message) bool {
	fields := []string{it.OrigFilename, it.ProposedFilename, it.IdentityKey}
	if msg != nil {
		fields = append(fields, msg.From, msg.Subject)
	}
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

// verdictFor collapses one item's local record into exactly one of G-5's
// five verdicts: "uploaded (uuid, verified-at)", "staged", "rejected",
// "already-in-portal (marked <date>)" or "unknown". "unknown" is returned
// only by Find itself (as "no match at all"); every item that exists
// locally maps to one of the other four. staged, approved,
// duplicate_linked, suppressed_peppol and failed all collapse to "staged"
// — G-5's vocabulary has no finer slot for "approved but not yet
// uploaded" or "flagged, awaiting an override", and all of them share the
// property that matters here: nothing has been sent, and nothing has been
// finally rejected.
func verdictFor(ctx context.Context, db *queue.DB, it *queue.Item) (string, error) {
	switch it.Status {
	case queue.StatusUploaded:
		if it.VerifiedAt != nil {
			return fmt.Sprintf("uploaded (uuid=%s, verified %s)", it.UUID, formatTime(*it.VerifiedAt)), nil
		}
		// F-37: a uuid with no verified_at is deliberately never retried —
		// surfaced distinctly so "uploaded" is never confused with "proven
		// delivered".
		return fmt.Sprintf("uploaded (unverified, uuid=%s)", it.UUID), nil
	case queue.StatusRejected:
		return "rejected", nil
	case queue.StatusAlreadyInPortal:
		markedAt := it.StagedAt
		if trs, err := db.Transitions(ctx, it.ID); err == nil {
			for i := len(trs) - 1; i >= 0; i-- {
				if trs[i].To == queue.StatusAlreadyInPortal {
					markedAt = trs[i].At
					break
				}
			}
		}
		return fmt.Sprintf("already-in-portal (marked %s)", markedAt.UTC().Format("2006-01-02")), nil
	default:
		return "staged", nil
	}
}

// FormatFind renders Find's result exactly as `postbode status --find`
// prints it (F-39, G-5): a genuinely scannable answer to "did this already
// get sent?", not a data dump. Zero matches prints the fifth G-5 verdict,
// "unknown", since a term with no local record at all is exactly what that
// verdict means.
func FormatFind(w io.Writer, term string, matches []FindMatch) {
	if len(matches) == 0 {
		_, _ = fmt.Fprintf(w, "unknown — no local record matches %q\n", term)
		return
	}

	_, _ = fmt.Fprintf(w, "%d match(es) for %q:\n\n", len(matches), term)
	for _, m := range matches {
		vendor := "(unknown sender)"
		subject := ""
		if m.Message != nil {
			if m.Message.From != "" {
				vendor = m.Message.From
			}
			subject = m.Message.Subject
		}
		name := m.Item.ProposedFilename
		if name == "" {
			name = m.Item.OrigFilename
		}
		_, _ = fmt.Fprintf(w, "[%d] %s — %q — %s\n", m.Item.ID, vendor, subject, name)
		_, _ = fmt.Fprintf(w, "    -> %s\n\n", m.Verdict)
	}
}
