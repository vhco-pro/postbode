package gmailwatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/vhco-pro/postbode/internal/queue"
	"golang.org/x/oauth2"
)

// isReauthError reports whether err is the shape of a Gmail-side auth
// failure F-16 must treat as routine rather than fatal: a refresh-token
// failure surfaced by the oauth2 transport (RFC 6749 token-endpoint errors
// such as invalid_grant). Any other error (network failure, a Gmail 5xx, a
// malformed response) is left to the caller's ordinary error path — F-16 is
// specifically about the OAuth refresh failing, not about Gmail being
// unreachable for other reasons.
func isReauthError(err error) (reason string, ok bool) {
	var rerr *oauth2.RetrieveError
	if errors.As(err, &rerr) {
		if rerr.ErrorCode != "" {
			return rerr.ErrorCode, true
		}
		return "oauth2 token refresh failed", true
	}
	return "", false
}

// handleReauth is F-16's core and never returns a fatal error. It persists
// the re-auth condition into sync_state (F-17, for Phase 10's `postbode
// status` to print), sends exactly one re-auth notification, and returns a
// PollResult flagging ReauthNeeded — leaving every queue row, and st's
// HistoryID/LastPollAt, completely untouched (AC-20: "leaves all queue rows
// untouched"). The very next poll tick calls Poll again and, once the
// underlying token source has recovered, resumes normally with no daemon
// restart required.
func (w *Watcher) handleReauth(ctx context.Context, st queue.SyncState, reason string) (PollResult, error) {
	st.LastAuthError = reason
	if err := w.DB.SaveSyncState(ctx, st); err != nil {
		return PollResult{}, fmt.Errorf("gmailwatch: persist re-auth state: %w", err)
	}
	if w.Notifier != nil {
		_ = w.Notifier.Notify(ctx, w.reauthNotification())
	}
	return PollResult{ReauthNeeded: true}, nil
}

// reauthNotification renders F-16's "one-click re-auth URL" notification.
// When OAuthConfig is set, the message embeds a real, ready-to-open consent
// URL; otherwise it falls back to a generic instruction.
func (w *Watcher) reauthNotification() string {
	if w.OAuthConfig != nil {
		url := w.OAuthConfig.AuthCodeURL("postbode-reauth", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
		return fmt.Sprintf("Postbode: Gmail needs re-authentication. Open this URL to continue: %s", url)
	}
	return "Postbode: Gmail needs re-authentication. Re-run the interactive auth flow to continue."
}
