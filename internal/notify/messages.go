package notify

import (
	"context"
	"fmt"
	"time"
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

// ParkedMessage is F-74's wording for a message that has exhausted its
// failure budget and been parked so the rest of the mailbox can drain.
//
// Two things it must say and does. First, that the message is NOT in the
// review queue: a user who reads "invoice problem" and opens the review UI
// to find nothing has been actively misled, because a parked message has no
// extractable document to review — extraction is precisely what failed.
// Second, the command that resolves it, per the same convention StagedMessage
// documents: an osascript notification is not clickable, so a notification
// without a command is a dead end.
//
// reason is already truncated and redacted by queue.RecordMessageFailure
// (NF-17); this clamps it again for the notification's own sake, because a
// 500-character macOS notification is unreadable.
func ParkedMessage(id string, failures int, reason string) string {
	return fmt.Sprintf(
		"Postbode: message %s could not be processed after %d attempts and has been set aside so the rest of your mail keeps flowing. It is NOT in the review queue. Reason: %s. Run: postbode status (then: postbode retry %s)",
		id, failures, clampForNotification(reason), id)
}

// PollStalled is F-82's wording for a daemon that is alive but not making
// progress — the condition that went unannounced for three days in the
// 2026-08-22 outage while `brew services list` cheerfully reported
// "started".
//
// since is the start of the current stall episode, not the last failure:
// "since 3 days ago" is the number that tells a human how bad this is.
func PollStalled(count int, since time.Time, lastErr string) string {
	return fmt.Sprintf(
		"Postbode: not making progress. %d consecutive polls have failed since %s. No new mail is being seen. Last error: %s. Run: postbode status",
		count, since.Local().Format("2 Jan 15:04"), clampForNotification(lastErr))
}

// clampForNotification keeps a notification readable. macOS truncates long
// notification bodies itself, silently and at an arbitrary point — better to
// cut deliberately and say so.
func clampForNotification(s string) string {
	const max = 140
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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
