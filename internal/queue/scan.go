package queue

import (
	"database/sql"
	"time"
)

// Timestamps are stored as RFC3339Nano text in UTC, written and parsed
// explicitly by this package rather than relying on driver-specific
// time.Time marshaling. SQLite's dynamic typing accepts TEXT in a column
// declared TIMESTAMP without complaint.
const timeLayout = time.RFC3339Nano

func timeToDB(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func timePtrToDB(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func stringOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// parseTime parses an RFC3339Nano-formatted NullString, returning the zero
// time when the column was NULL.
func parseTime(ns sql.NullString) (time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(timeLayout, ns.String)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// parseTimePtr is parseTime, but returns nil instead of a zero time so
// callers can distinguish "column was NULL" from "column held the zero
// time".
func parseTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := time.Parse(timeLayout, ns.String)
	if err != nil {
		return nil, err
	}
	t = t.UTC()
	return &t, nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting scanItem
// be reused for single-row and multi-row reads alike.
type rowScanner interface {
	Scan(dest ...any) error
}

const itemColumns = `
	id, gmail_message_id, spool_path, orig_filename, proposed_filename,
	mime_type, size_bytes, sha256, identity_key, identity_confidence,
	identity_source, status, needs_manual_handling, low_confidence,
	possible_duplicate, probably_already_handled, unsupported_type,
	linked_item_id, dedup_layer, staged_at, approved_at, uploaded_at,
	uuid, amount_of_pages, verified_at, failed_at, last_error, retry_count,
	next_retry_at, claimed_at, first_failed_at
`

func scanItem(rs rowScanner) (*Item, error) {
	var (
		it                                                     Item
		spoolPath, origFilename, proposedFilename, mimeType    sql.NullString
		sizeBytes                                              sql.NullInt64
		identityKey, identityConfidence, identitySource        sql.NullString
		status                                                 string
		linkedItemID                                           sql.NullInt64
		dedupLayer                                             sql.NullString
		stagedAt, approvedAt, uploadedAt                       sql.NullString
		uuidVal                                                sql.NullString
		amountOfPages                                          sql.NullInt64
		verifiedAt, failedAt                                   sql.NullString
		lastError                                              sql.NullString
		retryCount                                             sql.NullInt64
		nextRetryAt, claimedAt, firstFailedAt                  sql.NullString
		needsManualHandling, lowConfidence                     sql.NullInt64
		possibleDuplicate, probablyAlreadyHandled, unsupported sql.NullInt64
	)

	if err := rs.Scan(
		&it.ID, &it.GmailMessageID, &spoolPath, &origFilename, &proposedFilename,
		&mimeType, &sizeBytes, &it.SHA256, &identityKey, &identityConfidence,
		&identitySource, &status, &needsManualHandling, &lowConfidence,
		&possibleDuplicate, &probablyAlreadyHandled, &unsupported,
		&linkedItemID, &dedupLayer, &stagedAt, &approvedAt, &uploadedAt,
		&uuidVal, &amountOfPages, &verifiedAt, &failedAt, &lastError, &retryCount,
		&nextRetryAt, &claimedAt, &firstFailedAt,
	); err != nil {
		return nil, err
	}

	it.SpoolPath = spoolPath.String
	it.OrigFilename = origFilename.String
	it.ProposedFilename = proposedFilename.String
	it.MimeType = mimeType.String
	it.SizeBytes = sizeBytes.Int64
	it.IdentityKey = identityKey.String
	it.IdentityConfidence = identityConfidence.String
	it.IdentitySource = identitySource.String
	it.Status = Status(status)
	it.NeedsManualHandling = needsManualHandling.Int64 != 0
	it.LowConfidence = lowConfidence.Int64 != 0
	it.PossibleDuplicate = possibleDuplicate.Int64 != 0
	it.ProbablyAlreadyHandled = probablyAlreadyHandled.Int64 != 0
	it.UnsupportedType = unsupported.Int64 != 0
	it.DedupLayer = DedupLayer(dedupLayer.String)
	it.UUID = uuidVal.String
	it.AmountOfPages = int(amountOfPages.Int64)
	it.LastError = lastError.String
	it.RetryCount = int(retryCount.Int64)

	if linkedItemID.Valid {
		v := linkedItemID.Int64
		it.LinkedItemID = &v
	}

	var err error
	if it.StagedAt, err = parseTime(stagedAt); err != nil {
		return nil, err
	}
	if it.ApprovedAt, err = parseTimePtr(approvedAt); err != nil {
		return nil, err
	}
	if it.UploadedAt, err = parseTimePtr(uploadedAt); err != nil {
		return nil, err
	}
	if it.VerifiedAt, err = parseTimePtr(verifiedAt); err != nil {
		return nil, err
	}
	if it.FailedAt, err = parseTimePtr(failedAt); err != nil {
		return nil, err
	}
	if it.NextRetryAt, err = parseTimePtr(nextRetryAt); err != nil {
		return nil, err
	}
	if it.ClaimedAt, err = parseTimePtr(claimedAt); err != nil {
		return nil, err
	}
	if it.FirstFailedAt, err = parseTimePtr(firstFailedAt); err != nil {
		return nil, err
	}

	return &it, nil
}
