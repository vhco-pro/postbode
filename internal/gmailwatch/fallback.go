package gmailwatch

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/gmail/v1"
)

// FallbackQuery builds the F-13 windowed resync query for a watch scope
// (WatchAll or WatchInbox — anything else is treated as WatchAll, matching
// Watcher.effectiveWatch).
//
// `newer_than:{windowDays}d` is LOAD-BEARING (spec v1.3, ratified planner
// finding OQ-P6): omitting it would make a resync sweep years of archived
// mail and stage all of it on the first history gap. Do not drop it.
//
// The scope clause is what changed in v1.11. It used to be a flat `in:inbox`,
// which could not see mail that never carries the INBOX label — the POP3
// fetcher's imported messages (see watchscope.go) — so the fallback path had
// the same blind spot as the history path and could not act as its safety
// net. WatchAll instead names the exclusions directly. Gmail's default
// search scope already omits trash and spam, so `-in:trash -in:spam` are
// redundant with today's semantics and kept anyway: this query is the only
// written statement of what "in scope" means on the search side, and it must
// not silently disagree with Watcher.inScope on the label side.
func FallbackQuery(watch string, windowDays int) string {
	if windowDays <= 0 {
		windowDays = 30
	}
	scope := "-in:sent -in:draft -in:trash -in:spam"
	if strings.EqualFold(strings.TrimSpace(watch), WatchInbox) {
		scope = "in:inbox"
	}
	return fmt.Sprintf("%s newer_than:%dd (has:attachment OR invoice OR factuur)", scope, windowDays)
}

// fallbackList performs the F-13 fallback: on first run, or on any F-12
// history gap (ErrHistoryGap), list every candidate message with
// users.messages.list bounded by FallbackQuery, rather than the unbounded
// history this package prefers once a warm historyId is available. This is
// also what recovers a laptop that was asleep or offline for up to
// gmail.query_window_days (default 30, covering the 14-day-offline case,
// spec §8).
func (w *Watcher) fallbackList(ctx context.Context) ([]string, error) {
	var ids []string
	call := w.Service.Users.Messages.List(w.UserID).
		Q(FallbackQuery(w.effectiveWatch(), w.Config.QueryWindowDays)).
		Context(ctx)
	err := call.Pages(ctx, func(resp *gmail.ListMessagesResponse) error {
		for _, m := range resp.Messages {
			if m.Id != "" {
				ids = append(ids, m.Id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("gmailwatch: messages.list fallback: %w", err)
	}
	return ids, nil
}

// currentHistoryID reads the mailbox's current historyId via
// users.getProfile — used after a fallback sweep to establish a fresh
// baseline for the next poll's F-12 incremental sync.
func (w *Watcher) currentHistoryID(ctx context.Context) (string, error) {
	profile, err := w.Service.Users.GetProfile(w.UserID).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("gmailwatch: users.getProfile: %w", err)
	}
	return fmt.Sprintf("%d", profile.HistoryId), nil
}
