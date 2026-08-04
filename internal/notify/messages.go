package notify

import (
	"context"
	"fmt"
)

// StagedMessage is F-45's verbatim wording for a batch of count newly
// staged items awaiting review: "Postbode: N invoices waiting for
// review."
func StagedMessage(count int) string {
	return fmt.Sprintf("Postbode: %d invoices waiting for review.", count)
}

// UploadBatchCompleteMessage is F-45's second notification, sent when a
// batch of uploads completes. The spec quotes exact wording only for
// StagedMessage; this text is this implementation's choice for the
// upload-completion event.
func UploadBatchCompleteMessage(count int) string {
	return fmt.Sprintf("Postbode: upload batch complete, %d invoice(s) uploaded.", count)
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
