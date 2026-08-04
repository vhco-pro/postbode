package notify

import (
	"context"
	"fmt"
)

// StagedMessage is F-45's wording for a batch of count newly staged items
// awaiting review, extended with the command that actually opens them.
//
// The spec quotes "Postbode: N invoices waiting for review." verbatim, and
// that sentence alone is a dead end: a macOS notification posted by
// osascript is not clickable and cannot launch anything, so a user who sees
// it has been told something is waiting and given no way to reach it. The
// first real user hit exactly that and had to ask where the invoices were.
// The quoted sentence is preserved as the opening; the call to action is
// appended rather than replacing it.
//
// Also fixes the plural: "1 invoices" reads like a bug in the tool.
func StagedMessage(count int) string {
	noun := "invoices"
	if count == 1 {
		noun = "invoice"
	}
	return fmt.Sprintf("Postbode: %d %s waiting for review. Run: postbode review", count, noun)
}

// UploadBatchCompleteMessage is F-45's second notification, sent when a
// batch of uploads completes. The spec quotes exact wording only for
// StagedMessage; this text is this implementation's choice for the
// upload-completion event.
func UploadBatchCompleteMessage(count int) string {
	noun := "invoices"
	if count == 1 {
		noun = "invoice"
	}
	return fmt.Sprintf("Postbode: %d %s uploaded to the portal.", count, noun)
}

// NotifyStaged sends the F-45 staging notification exactly once for a
// batch of count newly staged items. Callers (gmailwatch/rules staging
// pipeline) must call this exactly once per staging batch, never once per
// item (AC-28).
func NotifyStaged(ctx context.Context, notifier Notifier, count int) error {
	return notifier.Notify(ctx, StagedMessage(count))
}

// NotifyUploadBatchComplete sends F-45's upload-completion notification
// exactly once for a batch of count uploads. Callers (the Phase 9
// uploader) must call this exactly once per completed batch (AC-28).
func NotifyUploadBatchComplete(ctx context.Context, notifier Notifier, count int) error {
	return notifier.Notify(ctx, UploadBatchCompleteMessage(count))
}
