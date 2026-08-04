package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RecordMessageIfNew is the F-30 (L1) mechanism: every processed
// gmail_message_id must be recorded before staging. It inserts the message
// row if this is the first time the id has been seen, or reports
// alreadySeen=true and does nothing otherwise. Callers (gmailwatch, Phase 7)
// must call this — and see alreadySeen=false — before extracting a
// message's attachments; a message already recorded here must never be
// re-extracted, regardless of history replay, restart or full resync.
func (db *DB) RecordMessageIfNew(ctx context.Context, msg Message) (alreadySeen bool, err error) {
	if msg.GmailMessageID == "" {
		return false, errors.New("queue: RecordMessageIfNew: gmail message id is required")
	}

	firstSeenAt := msg.FirstSeenAt
	if firstSeenAt.IsZero() {
		firstSeenAt = time.Now().UTC()
	}

	res, err := db.sqlDB.ExecContext(ctx, `
		INSERT INTO message (gmail_message_id, thread_id, "from", subject, internal_date, first_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(gmail_message_id) DO NOTHING
	`, msg.GmailMessageID, stringOrNil(msg.ThreadID), stringOrNil(msg.From), stringOrNil(msg.Subject),
		timeToDB(msg.InternalDate), timeToDB(firstSeenAt))
	if err != nil {
		return false, fmt.Errorf("queue: record message %s: %w", msg.GmailMessageID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("queue: record message %s: rows affected: %w", msg.GmailMessageID, err)
	}
	// ON CONFLICT DO NOTHING reports zero rows affected when the id already
	// existed — that is precisely "already seen".
	return n == 0, nil
}

// GetMessage reads one message by its gmail_message_id. Added for Phase 8
// (webui): the review list view shows sender and subject per item (F-43),
// which live on message, not item.
func (db *DB) GetMessage(ctx context.Context, gmailMessageID string) (*Message, error) {
	row := db.sqlDB.QueryRowContext(ctx, `
		SELECT gmail_message_id, thread_id, "from", subject, internal_date,
			first_seen_at, all_docs_uploaded_at, labeled_at
		FROM message WHERE gmail_message_id = ?
	`, gmailMessageID)

	var (
		m                            Message
		threadID, from, subject      sql.NullString
		internalDate, firstSeenAt    sql.NullString
		allDocsUploadedAt, labeledAt sql.NullString
	)
	if err := row.Scan(&m.GmailMessageID, &threadID, &from, &subject, &internalDate,
		&firstSeenAt, &allDocsUploadedAt, &labeledAt); err != nil {
		return nil, fmt.Errorf("queue: GetMessage(%s): %w", gmailMessageID, err)
	}
	m.ThreadID = threadID.String
	m.From = from.String
	m.Subject = subject.String

	var err error
	if m.InternalDate, err = parseTime(internalDate); err != nil {
		return nil, fmt.Errorf("queue: GetMessage(%s): parse internal_date: %w", gmailMessageID, err)
	}
	if m.FirstSeenAt, err = parseTime(firstSeenAt); err != nil {
		return nil, fmt.Errorf("queue: GetMessage(%s): parse first_seen_at: %w", gmailMessageID, err)
	}
	if m.AllDocsUploadedAt, err = parseTimePtr(allDocsUploadedAt); err != nil {
		return nil, fmt.Errorf("queue: GetMessage(%s): parse all_docs_uploaded_at: %w", gmailMessageID, err)
	}
	if m.LabeledAt, err = parseTimePtr(labeledAt); err != nil {
		return nil, fmt.Errorf("queue: GetMessage(%s): parse labeled_at: %w", gmailMessageID, err)
	}
	return &m, nil
}

// MarkMessageSubmitted stamps message.all_docs_uploaded_at and
// message.labeled_at with the same timestamp (F-14, AC-19). The uploader
// (Phase 9) calls this exactly once, immediately after the
// gmailwatch.ApplyLabel call that adds VH&Co/submitted and removes INBOX
// succeeds — so both columns are set together only once the label move
// actually happened, never speculatively. Idempotent: calling it again
// simply overwrites both timestamps, but the uploader's own
// labeled_at != nil check (via GetMessage) is what prevents a second
// ApplyLabel call from ever being issued in the first place.
func (db *DB) MarkMessageSubmitted(ctx context.Context, gmailMessageID string, at time.Time) error {
	res, err := db.sqlDB.ExecContext(ctx, `
		UPDATE message SET all_docs_uploaded_at = ?, labeled_at = ? WHERE gmail_message_id = ?
	`, timeToDB(at), timeToDB(at), gmailMessageID)
	if err != nil {
		return fmt.Errorf("queue: MarkMessageSubmitted(%s): %w", gmailMessageID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("queue: MarkMessageSubmitted(%s): rows affected: %w", gmailMessageID, err)
	}
	if n == 0 {
		return fmt.Errorf("queue: MarkMessageSubmitted(%s): message not found", gmailMessageID)
	}
	return nil
}

// MessageSeen reports whether gmailMessageID has already been recorded by
// RecordMessageIfNew (F-30, L1).
func (db *DB) MessageSeen(ctx context.Context, gmailMessageID string) (bool, error) {
	var exists int
	err := db.sqlDB.QueryRowContext(ctx, `SELECT 1 FROM message WHERE gmail_message_id = ?`, gmailMessageID).Scan(&exists)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("queue: check message seen %s: %w", gmailMessageID, err)
	default:
		return true, nil
	}
}
