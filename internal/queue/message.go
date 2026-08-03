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
