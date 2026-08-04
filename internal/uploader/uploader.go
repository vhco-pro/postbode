package uploader

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/vhco-pro/postbode/internal/clearfacts"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
)

// patInvalidMessage is F-51's mandated notification text for a 401/403
// upload failure ("PAT invalid or scope missing").
const patInvalidMessage = "Postbode: PAT invalid or scope missing."

// Clock lets tests (and, in principle, any caller) control "now" so backoff
// and give-up tests never sleep for real minutes/hours. A nil Clock on
// Uploader defaults to time.Now().UTC.
type Clock func() time.Time

// LabelApplier is the F-14 label-move primitive the uploader needs —
// satisfied by *gmailwatch.Watcher in production. Kept as a narrow
// interface here (rather than importing the whole gmailwatch.Watcher type
// as a struct field) so a test can substitute a minimal fake without
// spinning up a Gmail httptest server, though the AC-19 test exercises the
// real thing.
type LabelApplier interface {
	ApplyLabel(ctx context.Context, gmailMessageID, labelID string) error
}

// UploadClient is the subset of *clearfacts.Client the uploader calls.
// Declared as an interface so the AC-15/AC-17 tests can drive
// internal/clearfacts/fake through the real client (the normal case) while
// still allowing a hand-rolled double for edge cases if ever needed.
type UploadClient interface {
	UploadFile(ctx context.Context, in clearfacts.UploadInput) (*clearfacts.File, error)
	Document(ctx context.Context, id string) (*clearfacts.Document, error)
}

// Uploader is Postbode's uploader (spec §3.7, Phase 9). See doc.go for the
// full design rationale.
type Uploader struct {
	// DB is the queue store. Required.
	DB *queue.DB
	// Client uploads and verifies documents against ClearFacts. Required.
	Client UploadClient
	// CompanyNumber is sent as uploadFile's companyNumber argument (F-50,
	// A-12) — never the deprecated vatnumber.
	CompanyNumber string
	// LabelApplier issues the F-14 messages.modify call. Nil is a valid,
	// silent no-op (the label move simply never happens) — useful for tests
	// that only care about upload behaviour.
	LabelApplier LabelApplier
	// Notifier sends the F-45 upload-batch-complete notification and F-51's
	// PAT-invalid notification. Nil is a valid, silent no-op.
	Notifier notify.Notifier
	// Clock overrides time.Now().UTC for backoff/give-up computation and
	// verified_at/label timestamps. Nil defaults to time.Now().UTC.
	Clock Clock
	// Logf, when set, receives one line per notable event (verification
	// failure, unexpected DB error while continuing a batch). Nil is a
	// valid, silent default — see gmailwatch.Watcher.Logf for the same
	// convention.
	Logf func(format string, args ...any)
}

func (u *Uploader) now() time.Time {
	if u.Clock != nil {
		return u.Clock().UTC()
	}
	return time.Now().UTC()
}

func (u *Uploader) logf(format string, args ...any) {
	if u.Logf != nil {
		u.Logf(format, args...)
	}
}

// ReleaseStaleClaims releases every approved item whose claim is older than
// olderThan (F-52/AC-18): a process that claimed an item and then died
// before recording any outcome would otherwise strand that item, claimed
// forever, requiring a human to re-approve it. The daemon (out of this
// package's scope) is expected to call this once at startup — with a
// generous olderThan (long enough that a live uploader instance's own
// in-flight claim could never look "stale") — before running the uploader,
// and optionally again on a recurring tick.
func (u *Uploader) ReleaseStaleClaims(ctx context.Context, olderThan time.Duration) (int64, error) {
	return u.DB.ReleaseStaleClaims(ctx, u.now(), olderThan)
}

// UploadOne claims and processes at most one approved-and-due item.
// processed reports whether an item was claimed at all (true even if
// processing it ended in a retryable failure or a terminal failed status —
// "processed" means "the claim slot was used," not "it uploaded"). err is
// only ever a genuine infrastructure failure (a DB error); classified
// upload outcomes (retryable failure, terminal failure) are handled
// internally and never surface as err.
func (u *Uploader) UploadOne(ctx context.Context) (processed bool, err error) {
	item, err := u.DB.ClaimApprovedDue(ctx, u.now())
	if err != nil {
		return false, fmt.Errorf("uploader: claim: %w", err)
	}
	if item == nil {
		return false, nil
	}
	if err := u.processItem(ctx, item); err != nil {
		return true, err
	}
	return true, nil
}

// RunBatch drains every currently-due approved item, one at a time (F-54's
// max_concurrent/min_interval pacing is enforced inside Client, not here —
// see doc.go), and sends the F-45 upload-batch-complete notification exactly
// once, only if at least one item actually reached uploaded. uploadedCount
// counts items that ended this batch in status=uploaded; a retryable
// failure (left approved, to retry later) and a terminal failure both
// leave uploadedCount unchanged. A single item's unexpected DB error is
// logged and does not abort the rest of the batch — durable approval
// (F-52) means every other approved item still gets its own attempt.
func (u *Uploader) RunBatch(ctx context.Context) (uploadedCount int, err error) {
	for {
		item, claimErr := u.DB.ClaimApprovedDue(ctx, u.now())
		if claimErr != nil {
			return uploadedCount, fmt.Errorf("uploader: claim: %w", claimErr)
		}
		if item == nil {
			break
		}
		if procErr := u.processItem(ctx, item); procErr != nil {
			u.logf("uploader: item %d: unexpected error, continuing batch: %v", item.ID, procErr)
			continue
		}
		updated, getErr := u.DB.GetItem(ctx, item.ID)
		if getErr != nil {
			u.logf("uploader: item %d: re-read after processing failed: %v", item.ID, getErr)
			continue
		}
		if updated.Status == queue.StatusUploaded {
			uploadedCount++
		}
	}
	if uploadedCount > 0 && u.Notifier != nil {
		if err := notify.NotifyUploadBatchComplete(ctx, u.Notifier, uploadedCount); err != nil {
			u.logf("uploader: upload-batch-complete notification failed: %v", err)
		}
	}
	return uploadedCount, nil
}

// processItem uploads one already-claimed item and drives it to its next
// state: uploaded (+ proof-of-delivery + label check), a scheduled retry
// still in approved, or terminal failed. Returns an error only for a DB
// write failure — never for a classified upload outcome.
func (u *Uploader) processItem(ctx context.Context, item *queue.Item) error {
	f, err := os.Open(item.SpoolPath)
	if err != nil {
		// The spool file is gone or unreadable — not a ClearFacts error at
		// all, and not retryable by waiting (the bytes are not coming back).
		// Terminal failed, same as any other non-retryable error.
		return u.DB.MarkFailed(ctx, item.ID, fmt.Sprintf("open spool file: %v", err))
	}
	defer func() { _ = f.Close() }()

	file, uploadErr := u.Client.UploadFile(ctx, clearfacts.UploadInput{
		CompanyNumber:  u.CompanyNumber,
		Filename:       uploadFilename(item),
		ItemID:         strconv.FormatInt(item.ID, 10),
		GmailMessageID: item.GmailMessageID,
		SHA256:         item.SHA256,
		File:           f,
	})
	if uploadErr != nil {
		return u.handleUploadError(ctx, item, uploadErr)
	}

	if err := u.DB.MarkUploaded(ctx, item.ID, file.UUID, file.AmountOfPages); err != nil {
		return fmt.Errorf("mark uploaded: %w", err)
	}

	u.verify(ctx, item.ID, file.UUID)

	if err := u.maybeApplyLabel(ctx, item.GmailMessageID); err != nil {
		// The label move failing (Gmail transiently unreachable, etc.) must
		// never undo or retry the upload itself — F-14 is best-effort on top
		// of an already-successful, already-verified-or-not upload. Log and
		// move on; the next successful upload for this message (or a future
		// reconciliation pass) will find all-uploaded still true and retry
		// the label call.
		u.logf("uploader: message %s: label move failed: %v", item.GmailMessageID, err)
	}

	return nil
}

// uploadFilename prefers the F-23 proposed filename, falling back to the
// original attachment filename when no proposal was computed.
func uploadFilename(item *queue.Item) string {
	if item.ProposedFilename != "" {
		return item.ProposedFilename
	}
	return item.OrigFilename
}

// handleUploadError classifies uploadErr (via clearfacts.IsRetryable /
// IsNotifyWorthy, themselves backed by clearfacts.Classify) and applies
// F-51's rules:
//
//   - 401/403 (NotifyWorthy): notify, then terminal failed, no retry.
//   - any other non-retryable 4xx: terminal failed immediately, no retry,
//     retry_count stays 0 (AC-17's "400 ... retry_count == 0").
//   - 429/5xx/network (retryable): RecordUploadRetry (stays approved,
//     released for the next due claim); if that push crosses the F-51 24h
//     give-up threshold, additionally terminal-fail it with the same error.
func (u *Uploader) handleUploadError(ctx context.Context, item *queue.Item, uploadErr error) error {
	if clearfacts.IsNotifyWorthy(uploadErr) && u.Notifier != nil {
		if err := u.Notifier.Notify(ctx, patInvalidMessage); err != nil {
			u.logf("uploader: item %d: PAT-invalid notification failed: %v", item.ID, err)
		}
	}

	if !clearfacts.IsRetryable(uploadErr) {
		if err := u.DB.MarkFailed(ctx, item.ID, uploadErr.Error()); err != nil {
			return fmt.Errorf("mark failed (terminal): %w", err)
		}
		return nil
	}

	now := u.now()
	nextAttempt := item.RetryCount + 1
	nextRetryAt := now.Add(clearfacts.Backoff(nextAttempt))
	if err := u.DB.RecordUploadRetry(ctx, item.ID, uploadErr.Error(), nextRetryAt, now); err != nil {
		return fmt.Errorf("record upload retry: %w", err)
	}

	updated, err := u.DB.GetItem(ctx, item.ID)
	if err != nil {
		return fmt.Errorf("re-read after recording retry: %w", err)
	}
	if updated.FirstFailedAt != nil && clearfacts.ShouldGiveUp(u.now().Sub(*updated.FirstFailedAt)) {
		if err := u.DB.MarkFailed(ctx, item.ID, uploadErr.Error()); err != nil {
			return fmt.Errorf("mark failed (give up after 24h): %w", err)
		}
	}
	return nil
}

// verify implements F-37's proof-of-delivery step: call document(id:) once
// and, on a resolving response, record verified_at. Any failure here
// (network error, non-resolving document) is logged and swallowed — never
// retried, never fails the item — leaving it displayed as
// "uploaded (unverified)" per ADR-003.
func (u *Uploader) verify(ctx context.Context, itemID int64, uuid string) {
	doc, err := u.Client.Document(ctx, uuid)
	if err != nil || doc == nil {
		u.logf("uploader: item %d: verify(%s) did not resolve, leaving unverified: %v", itemID, uuid, err)
		return
	}
	if err := u.DB.MarkVerified(ctx, itemID, u.now()); err != nil {
		u.logf("uploader: item %d: MarkVerified failed: %v", itemID, err)
	}
}

// maybeApplyLabel implements F-14/AC-19: only when every item extracted
// from gmailMessageID has reached status=uploaded does it call
// LabelApplier.ApplyLabel and stamp message.all_docs_uploaded_at/labeled_at
// together. Idempotent via the labeled_at check — a message already labeled
// is never modified again.
func (u *Uploader) maybeApplyLabel(ctx context.Context, gmailMessageID string) error {
	msg, err := u.DB.GetMessage(ctx, gmailMessageID)
	if err != nil {
		return fmt.Errorf("get message: %w", err)
	}
	if msg.LabeledAt != nil {
		return nil
	}

	items, err := u.DB.ItemsByMessageID(ctx, gmailMessageID)
	if err != nil {
		return fmt.Errorf("list items by message: %w", err)
	}
	if len(items) == 0 {
		return nil
	}
	for _, it := range items {
		if it.Status != queue.StatusUploaded {
			// At least one document has not reached terminal uploaded —
			// never modify the message yet (F-14, AC-19).
			return nil
		}
	}

	if u.LabelApplier == nil {
		return nil
	}

	st, err := u.DB.GetSyncState(ctx)
	if err != nil {
		return fmt.Errorf("get sync state: %w", err)
	}
	if st.LabelIDSubmitted == "" {
		return fmt.Errorf("no submitted label id resolved in sync_state")
	}

	if err := u.LabelApplier.ApplyLabel(ctx, gmailMessageID, st.LabelIDSubmitted); err != nil {
		return fmt.Errorf("apply label: %w", err)
	}

	if err := u.DB.MarkMessageSubmitted(ctx, gmailMessageID, u.now()); err != nil {
		return fmt.Errorf("mark message submitted: %w", err)
	}
	return nil
}
