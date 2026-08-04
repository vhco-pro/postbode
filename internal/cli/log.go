package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// LogEntry is one printable line of the combined decision + upload log
// (F-28, F-65). Message is pre-rendered and is guaranteed never to contain
// a message body or attachment content — see this package's doc comment.
type LogEntry struct {
	At      time.Time
	Kind    string // "decision" or "transition"
	Message string
}

// BuildLog assembles the combined decision + upload log (F-28, F-65),
// discovering every gmail_message_id reachable from an item (see this
// package's doc comment for the resulting gap: messages that produced zero
// items — a rules-engine "denied" or "dropped" decision — are not
// currently reachable through internal/queue's public API and so do not
// appear here). since filters to entries at or after now.Add(-since);
// since <= 0 means "no filter".
func BuildLog(ctx context.Context, db *queue.DB, since time.Duration, now time.Time) ([]LogEntry, error) {
	var cutoff time.Time
	if since > 0 {
		cutoff = now.Add(-since)
	}

	var entries []LogEntry
	seenMessage := make(map[string]bool)

	for _, st := range allStatuses {
		items, err := db.ListByStatus(ctx, st)
		if err != nil {
			return nil, fmt.Errorf("cli: BuildLog: %w", err)
		}
		for _, it := range items {
			if !seenMessage[it.GmailMessageID] {
				seenMessage[it.GmailMessageID] = true
				decisions, err := db.DecisionsByMessageID(ctx, it.GmailMessageID)
				if err != nil {
					return nil, fmt.Errorf("cli: BuildLog: %w", err)
				}
				for _, d := range decisions {
					if !cutoff.IsZero() && d.At.Before(cutoff) {
						continue
					}
					rule := "-"
					if d.MatchedRuleIndex != nil {
						rule = fmt.Sprintf("%d", *d.MatchedRuleIndex)
					}
					entries = append(entries, LogEntry{
						At:   d.At,
						Kind: "decision",
						Message: fmt.Sprintf("decision=%s message=%s rule=%s reason=%q",
							d.Decision, it.GmailMessageID, rule, d.Reason),
					})
				}
			}

			trs, err := db.Transitions(ctx, it.ID)
			if err != nil {
				return nil, fmt.Errorf("cli: BuildLog: %w", err)
			}
			for _, tr := range trs {
				if !cutoff.IsZero() && tr.At.Before(cutoff) {
					continue
				}
				from := "-"
				if tr.From != "" {
					from = string(tr.From)
				}
				entries = append(entries, LogEntry{
					At:   tr.At,
					Kind: "transition",
					Message: fmt.Sprintf("item=%d %s->%s actor=%s",
						it.ID, from, tr.To, tr.Actor),
				})
			}
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].At.Before(entries[j].At) })
	return entries, nil
}

// FormatLog renders entries exactly as `postbode log` prints them: one
// line per entry, RFC3339 timestamp first so the output sorts and greps
// naturally.
func FormatLog(w io.Writer, entries []LogEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "(no log entries)")
		return
	}
	for _, e := range entries {
		fmt.Fprintf(w, "%s  %-10s %s\n", formatTime(e.At), e.Kind, e.Message)
	}
}
