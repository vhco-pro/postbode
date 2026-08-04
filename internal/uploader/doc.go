// Package uploader is Postbode's uploader (spec §3.7, plan Phase 9): it
// turns approved queue items into ClearFacts uploads, with durable
// approval, retry/backoff, proof of delivery and the F-14 label move — the
// place where G-2 (nothing uploaded twice) and G-3 (nothing uploaded
// without human approval) are structurally enforced.
//
// # Item selection (G-3)
//
// The uploader's only item source is queue.DB.ClaimApprovedDue, which is
// literally `WHERE status = 'approved' AND claimed_at IS NULL AND
// (next_retry_at IS NULL OR next_retry_at <= now)`. There is no other path
// into UploadOne/RunBatch — no auto-approve, no "upload staged items"
// shortcut. Approving an item (internal/webui, actor human) is the only way
// an item ever becomes claimable.
//
// # Exactly-once via claim atomicity (F-53)
//
// ClaimApproved/ClaimApprovedDue (internal/queue, Phase 4) already select
// and mark an item claimed inside one transaction against a single-
// connection *sql.DB, so a concurrent or restarted uploader process cannot
// observe and claim the same row twice — see queue.ClaimApproved's doc
// comment for the full argument. This package builds on that primitive
// rather than re-implementing locking: by the time UploadOne calls
// UploadFile, the item is already durably marked claimed and no other
// uploader instance can claim it too.
//
// # Durable approval (F-52) and retry/backoff (F-51)
//
// A retryable failure (network error, 429, 5xx) does not fail the item: it
// calls queue.DB.RecordUploadRetry, which increments retry_count, records
// the error, schedules next_retry_at via clearfacts.Backoff, and — crucially
// — releases the claim and leaves status=approved. The item requires no
// re-approval; the next RunBatch (this process or a restarted one) claims it
// again once next_retry_at is reached. A terminal error (any 4xx except 429,
// classified via clearfacts.Classify) or crossing the 24h give-up threshold
// (clearfacts.ShouldGiveUp, anchored to item.FirstFailedAt) moves the item to
// status=failed via queue.DB.MarkFailed, recording the last error.
//
// Killing the process between approval and upload is exactly the case
// ClaimApproved's atomicity and RecordUploadRetry's claim release exist for:
// on restart, the item is still approved (never touched), gets re-claimed,
// and uploads exactly once (AC-18).
//
// # Proof of delivery (F-37) — deliberately not retried
//
// After a successful UploadFile, the item is marked uploaded with its uuid.
// The uploader then calls clearfacts.Client.Document(uuid) once and, on a
// resolving response, records verified_at. A verification failure is never
// retried and never fails the item: an item with a uuid but no verified_at
// displays as "uploaded (unverified)" (spec ADR-003) — retrying a
// verification-only failure risks a second real portal upload, which
// violates G-2 (the invariant this whole package exists to protect) harder
// than an unverified row does.
//
// # Label move (F-14, AC-19)
//
// After every successful upload, the uploader checks whether every item
// belonging to the same gmail_message_id is now status=uploaded. Only then
// does it call the injected LabelApplier (normally *gmailwatch.Watcher) to
// add the resolved VH&Co/submitted label and remove INBOX, and stamp
// message.all_docs_uploaded_at / labeled_at together via
// queue.DB.MarkMessageSubmitted — which also makes the check idempotent: a
// message already labeled is never re-modified.
package uploader
