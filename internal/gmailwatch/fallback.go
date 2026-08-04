package gmailwatch

import (
	"context"
	"fmt"

	"google.golang.org/api/gmail/v1"
)

// FallbackQuery builds the F-13 windowed resync query. `in:inbox` and
// `newer_than:{windowDays}d` are LOAD-BEARING (spec v1.3, ratified planner
// finding OQ-P6): omitting either would make a resync sweep the entire
// archived mailbox, contradicting F-11's whole-INBOX (not whole-mailbox)
// watch scope, and potentially stage years of old invoices on the first
// history gap. Do not simplify this string.
func FallbackQuery(windowDays int) string {
	if windowDays <= 0 {
		windowDays = 30
	}
	return fmt.Sprintf("in:inbox newer_than:%dd (has:attachment OR invoice OR factuur)", windowDays)
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
		Q(FallbackQuery(w.Config.QueryWindowDays)).
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
