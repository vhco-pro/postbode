package gmailwatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
)

// ErrHistoryGap is returned by historySync when Gmail reports the stored
// historyId is too old to resume from (HTTP 404 from users.history.list) —
// the trigger for the F-13 windowed fallback.
var ErrHistoryGap = errors.New("gmailwatch: history gap: historyId not found")

// historySync performs the F-12 incremental poll: users.history.list from
// startHistoryID, restricted to messageAdded events only — Postbode only
// cares about new mail arriving, never about label or deletion history. It
// returns every added message id (deduplicated across pages, in case a
// message appears in more than one history record) and the newest historyId
// seen, ready for persistence into sync_state for the next poll.
//
// It deliberately sends NO labelId parameter. The F-11 watch scope is
// enforced by Watcher.inScope against the fetched message's real labels
// instead — see watchscope.go for the live evidence that labelId does not
// mean what it appears to mean here, and silently dropped imported mail.
func (w *Watcher) historySync(ctx context.Context, startHistoryID uint64) (msgIDs []string, newHistoryID string, err error) {
	seen := make(map[string]bool)
	var latest uint64

	call := w.Service.Users.History.List(w.UserID).
		StartHistoryId(startHistoryID).
		HistoryTypes("messageAdded").
		Context(ctx)

	err = call.Pages(ctx, func(resp *gmail.ListHistoryResponse) error {
		for _, h := range resp.History {
			for _, added := range h.MessagesAdded {
				if added.Message == nil || added.Message.Id == "" {
					continue
				}
				if !seen[added.Message.Id] {
					seen[added.Message.Id] = true
					msgIDs = append(msgIDs, added.Message.Id)
				}
			}
		}
		if resp.HistoryId > latest {
			latest = resp.HistoryId
		}
		return nil
	})
	if err != nil {
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == 404 {
			return nil, "", ErrHistoryGap
		}
		return nil, "", fmt.Errorf("gmailwatch: history.list: %w", err)
	}

	if latest == 0 {
		// No history record carried a historyId (nothing changed since
		// startHistoryID) — carry the caller's own baseline forward
		// unchanged rather than losing it.
		return msgIDs, strconv.FormatUint(startHistoryID, 10), nil
	}
	return msgIDs, strconv.FormatUint(latest, 10), nil
}
