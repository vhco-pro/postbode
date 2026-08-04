package gmailwatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/api/gmail/v1"
)

// ApplyLabel issues the F-14 messages.modify call: add labelID (the
// resolved VH&Co/submitted label) and remove INBOX. This is a callable
// primitive only — deciding WHEN to call it (only once every document
// extracted from a message has reached terminal uploaded, AC-19) is the
// Phase 9 uploader's job per the plan's Context deviations table; this
// package does not wire that trigger.
func (w *Watcher) ApplyLabel(ctx context.Context, gmailMessageID, labelID string) error {
	if labelID == "" {
		return errors.New("gmailwatch: ApplyLabel: labelID is required")
	}
	_, err := w.Service.Users.Messages.Modify(w.UserID, gmailMessageID, &gmail.ModifyMessageRequest{
		AddLabelIds:    []string{labelID},
		RemoveLabelIds: []string{"INBOX"},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("gmailwatch: apply label to message %s: %w", gmailMessageID, err)
	}
	return nil
}

// ResolveAndPersistSubmittedLabel resolves w.Config.SubmittedLabel (F-15,
// defaulting to SubmittedLabelName) via ResolveLabel and, on success,
// persists its id into sync_state.label_id_submitted. It preserves
// ResolveLabel's three-way contract exactly:
//
//   - success: label id persisted, nil returned.
//   - ErrLabelNotFound (an authenticated list came back without the name):
//     returned verbatim. The caller (the daemon's startup sequence, out of
//     this package's scope) must refuse to start the uploader on this
//     error while leaving Watcher free to keep polling and staging — F-15
//     scopes the hard refusal to the uploader, not to the whole daemon.
//   - any other error (auth failure, network failure, pending re-auth):
//     returned wrapped, deliberately NOT ErrLabelNotFound — resolution is
//     deferred, not refused (F-15 v1.3 / OQ-P7).
func (w *Watcher) ResolveAndPersistSubmittedLabel(ctx context.Context) error {
	name := w.Config.SubmittedLabel
	if name == "" {
		name = SubmittedLabelName
	}
	label, err := ResolveLabel(ctx, w.Service, w.UserID, name)
	if err != nil {
		if errors.Is(err, ErrLabelNotFound) {
			return err
		}
		return fmt.Errorf("gmailwatch: resolve submitted label: %w", err)
	}

	st, err := w.DB.GetSyncState(ctx)
	if err != nil {
		return fmt.Errorf("gmailwatch: resolve submitted label: read sync_state: %w", err)
	}
	st.LabelIDSubmitted = label.Id
	if err := w.DB.SaveSyncState(ctx, st); err != nil {
		return fmt.Errorf("gmailwatch: resolve submitted label: persist sync_state: %w", err)
	}
	return nil
}

// RecordTokenIssued persists issuedAt into sync_state.token_issued_at
// (F-17) and clears any earlier re-auth condition — a freshly (re-)issued
// token means whatever prompted F-16's notification has been resolved by
// the human completing the consent flow. Called by the daemon (out of this
// package's scope) immediately after AuthenticateInteractive succeeds.
func (w *Watcher) RecordTokenIssued(ctx context.Context, issuedAt time.Time) error {
	st, err := w.DB.GetSyncState(ctx)
	if err != nil {
		return fmt.Errorf("gmailwatch: record token issued: read sync_state: %w", err)
	}
	t := issuedAt.UTC()
	st.TokenIssuedAt = &t
	st.LastAuthError = ""
	if err := w.DB.SaveSyncState(ctx, st); err != nil {
		return fmt.Errorf("gmailwatch: record token issued: persist sync_state: %w", err)
	}
	return nil
}
