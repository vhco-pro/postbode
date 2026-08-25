package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// messageFailureColumns is the canonical select list, so every scan path
// agrees on column order.
const messageFailureColumns = `gmail_message_id, failure_count, first_failed_at, last_failed_at,
	last_error, parked_at, park_count, retry_count, next_retry_at, notified_at`

// RecordMessageFailure records one failed processing attempt for id and
// reports whether that attempt parked the message (F-70, F-72).
//
// budget is the number of consecutive failures tolerated before parking.
// Callers pass F-76's effective budget of 1 for a message that has been
// parked before — a message that has already proven itself non-transient has
// forfeited the presumption of transience, and must never cost another full
// budget window of stalled mailbox.
//
// parked is true EXACTLY ONCE per park, on the attempt that crosses the
// threshold, which is what lets the caller satisfy F-74's notify-once
// without tracking notification state itself. A later failure of an
// already-parked message re-parks it (re-arming the retry schedule) and
// returns parked=false, because the human has already been told.
//
// now is injected rather than read from the clock so cooldown behaviour is
// testable without sleeping (mirrors uploader.Clock).
func (db *DB) RecordMessageFailure(ctx context.Context, id, errText string, budget int, now time.Time, cooldown time.Duration) (MessageFailure, bool, error) {
	if id == "" {
		return MessageFailure{}, false, errors.New("queue: RecordMessageFailure: gmail message id is required")
	}
	if budget < 1 {
		// A budget below 1 would park on a failure that never happened.
		// config rejects <= 0 at load (F-87); this is the defence in depth.
		budget = 1
	}
	now = now.UTC()

	cur, err := db.GetMessageFailure(ctx, id)
	if err != nil {
		return MessageFailure{}, false, err
	}

	f := MessageFailure{GmailMessageID: id, FirstFailedAt: now}
	if cur != nil {
		f = *cur
	}
	f.FailureCount++
	f.LastFailedAt = now
	f.LastError = TruncateError(errText)

	// The park decision. Note the >= rather than ==: an operator lowering
	// failure_budget between polls must not leave an over-budget message
	// unparked forever.
	newlyParked := false
	if !f.Parked() && f.FailureCount >= budget {
		parkedAt := now
		f.ParkedAt = &parkedAt
		f.ParkCount++
		newlyParked = true
		f.NotifiedAt = &parkedAt
		next := now.Add(cooldown)
		f.NextRetryAt = &next
	} else if f.Parked() {
		// A re-park: an already-parked message failed again on a retry
		// attempt. Do NOT notify again — the park is already known.
		//
		// And deliberately do NOT touch NextRetryAt. The automatic schedule
		// has exactly one owner, RecordRetryAttempt, which already advanced
		// it (or set it to NULL for "exhausted") when this attempt was
		// admitted. Re-arming it here would silently undo that exhaustion
		// and give a permanently broken message an unbounded supply of
		// retries — which is precisely the "re-wedge the poll on a schedule
		// forever" behaviour F-75 exists to prevent.
		f.ParkCount++
	}

	if err := db.upsertMessageFailure(ctx, f); err != nil {
		return MessageFailure{}, false, err
	}
	return f, newlyParked, nil
}

func (db *DB) upsertMessageFailure(ctx context.Context, f MessageFailure) error {
	_, err := db.sqlDB.ExecContext(ctx, `
		INSERT INTO message_failure (`+messageFailureColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(gmail_message_id) DO UPDATE SET
			failure_count   = excluded.failure_count,
			last_failed_at  = excluded.last_failed_at,
			last_error      = excluded.last_error,
			parked_at       = excluded.parked_at,
			park_count      = excluded.park_count,
			retry_count     = excluded.retry_count,
			next_retry_at   = excluded.next_retry_at,
			notified_at     = excluded.notified_at
	`,
		f.GmailMessageID, f.FailureCount, timeToDB(f.FirstFailedAt), timeToDB(f.LastFailedAt),
		stringOrNil(f.LastError), timePtrToDB(f.ParkedAt), f.ParkCount, f.RetryCount,
		timePtrToDB(f.NextRetryAt), timePtrToDB(f.NotifiedAt),
	)
	if err != nil {
		return fmt.Errorf("queue: record message failure %s: %w", f.GmailMessageID, err)
	}
	return nil
}

// ClearMessageFailure deletes id's failure row, which is how F-70's "reset
// to zero on any successful processing" is implemented: the absence of a row
// IS a zero count, so there is no stale-counter state to get wrong.
//
// Deleting an absent row is not an error — the overwhelmingly common case is
// a healthy message that never had one.
func (db *DB) ClearMessageFailure(ctx context.Context, id string) error {
	if _, err := db.sqlDB.ExecContext(ctx, `DELETE FROM message_failure WHERE gmail_message_id = ?`, id); err != nil {
		return fmt.Errorf("queue: clear message failure %s: %w", id, err)
	}
	return nil
}

// GetMessageFailure returns id's failure row, or (nil, nil) when there is
// none. A missing row is the normal, healthy state, not an error.
func (db *DB) GetMessageFailure(ctx context.Context, id string) (*MessageFailure, error) {
	row := db.sqlDB.QueryRowContext(ctx,
		`SELECT `+messageFailureColumns+` FROM message_failure WHERE gmail_message_id = ?`, id)
	f, err := scanMessageFailure(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: get message failure %s: %w", id, err)
	}
	return f, nil
}

// ListParkedMessages returns every parked message, oldest park first, so
// `postbode status` reports the longest-unresolved problem at the top.
//
// Nothing filters or ages this list: under F-79 a parked message leaves it
// only by being processed successfully or by explicit human action. A
// 90-day-old park is not stale, it is unresolved.
func (db *DB) ListParkedMessages(ctx context.Context) ([]MessageFailure, error) {
	rows, err := db.sqlDB.QueryContext(ctx,
		`SELECT `+messageFailureColumns+` FROM message_failure
		 WHERE parked_at IS NOT NULL ORDER BY parked_at, gmail_message_id`)
	if err != nil {
		return nil, fmt.Errorf("queue: list parked messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []MessageFailure
	for rows.Next() {
		f, serr := scanMessageFailure(rows)
		if serr != nil {
			return nil, fmt.Errorf("queue: list parked messages: %w", serr)
		}
		out = append(out, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: list parked messages: %w", err)
	}
	return out, nil
}

// FailedIDs returns the set of message ids that currently have a failure row
// of any kind, parked or not.
//
// This exists so a poll can reset F-70's counter without paying a write per
// healthy message. The alternative — deleting unconditionally on every
// success — is one write transaction per message per poll for a mailbox
// that is almost always entirely healthy (NF-18). The alternative to THAT —
// tracking failed ids in process memory — is wrong across a restart: the
// map would be empty while the rows persisted, so a stale count could park
// an innocent message later. One small read per poll costs neither.
func (db *DB) FailedIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `SELECT gmail_message_id FROM message_failure`)
	if err != nil {
		return nil, fmt.Errorf("queue: failed ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("queue: failed ids: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: failed ids: %w", err)
	}
	return out, nil
}

// DueRetries returns the ids of parked messages whose next attempt is due at
// or before now (F-75, F-77). This is the single admission mechanism behind
// BOTH automatic and manual retry: parking advances history_id past the
// message, so a parked message is never listed again and can only re-enter
// the poll by id through here.
func (db *DB) DueRetries(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `
		SELECT gmail_message_id FROM message_failure
		WHERE parked_at IS NOT NULL AND next_retry_at IS NOT NULL AND next_retry_at <= ?
		ORDER BY next_retry_at, gmail_message_id
	`, timeToDB(now.UTC()))
	if err != nil {
		return nil, fmt.Errorf("queue: due retries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("queue: due retries: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: due retries: %w", err)
	}
	return ids, nil
}

// Unpark makes a parked message due on the next poll (F-77, `postbode
// retry <id>`). It reports whether anything changed, so the CLI can exit
// non-zero for an id that is unknown or not parked rather than silently
// claiming success.
//
// It deliberately does NOT reset failure_count, park_count or retry_count:
// those are the history that keeps F-76's effective-budget-of-1 and F-75's
// attempt bound working. A human saying "try again" is not a claim that the
// message was never broken.
func (db *DB) Unpark(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := db.sqlDB.ExecContext(ctx, `
		UPDATE message_failure SET next_retry_at = ?
		WHERE gmail_message_id = ? AND parked_at IS NOT NULL
	`, timeToDB(now.UTC()), id)
	if err != nil {
		return false, fmt.Errorf("queue: unpark %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("queue: unpark %s: rows affected: %w", id, err)
	}
	return n > 0, nil
}

// UnparkAll is Unpark for every parked message (`postbode retry --all`),
// returning how many were made due.
func (db *DB) UnparkAll(ctx context.Context, now time.Time) (int, error) {
	res, err := db.sqlDB.ExecContext(ctx, `
		UPDATE message_failure SET next_retry_at = ? WHERE parked_at IS NOT NULL
	`, timeToDB(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("queue: unpark all: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("queue: unpark all: rows affected: %w", err)
	}
	return int(n), nil
}

// RecordRetryAttempt marks that an automatic attempt has been spent for id
// and schedules the next one per F-75: the cooldown doubles each time,
// capped at MaxRetryCooldown, for at most attempts tries. Once exhausted,
// next_retry_at becomes NULL — the message stays parked and stays reported,
// but generates no further automatic work until a human runs `postbode
// retry`.
func (db *DB) RecordRetryAttempt(ctx context.Context, id string, now time.Time, cooldown time.Duration, attempts int) error {
	cur, err := db.GetMessageFailure(ctx, id)
	if err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	cur.RetryCount++
	if cur.RetryCount >= attempts {
		cur.NextRetryAt = nil
	} else {
		next := now.UTC().Add(backoff(cooldown, cur.RetryCount))
		cur.NextRetryAt = &next
	}
	return db.upsertMessageFailure(ctx, *cur)
}

// MaxRetryCooldown caps F-75's doubling backoff. Past a day, waiting longer
// buys nothing: the failure has long since proven itself non-transient, and
// the message is already reported and awaiting a human either way.
const MaxRetryCooldown = 24 * time.Hour

// backoff is the interval before automatic attempt n+1, doubling from base
// and capped. Overflow-safe: the doubling stops at the cap rather than
// wrapping.
func backoff(base time.Duration, n int) time.Duration {
	d := base
	for i := 0; i < n; i++ {
		if d >= MaxRetryCooldown/2 {
			return MaxRetryCooldown
		}
		d *= 2
	}
	if d > MaxRetryCooldown {
		return MaxRetryCooldown
	}
	return d
}

func scanMessageFailure(rs rowScanner) (*MessageFailure, error) {
	var (
		f                                 MessageFailure
		firstFailedAt, lastFailedAt       sql.NullString
		parkedAt, nextRetryAt, notifiedAt sql.NullString
		lastError                         sql.NullString
	)
	if err := rs.Scan(&f.GmailMessageID, &f.FailureCount, &firstFailedAt, &lastFailedAt,
		&lastError, &parkedAt, &f.ParkCount, &f.RetryCount, &nextRetryAt, &notifiedAt); err != nil {
		return nil, err
	}
	f.LastError = lastError.String

	var err error
	if f.FirstFailedAt, err = parseTime(firstFailedAt); err != nil {
		return nil, fmt.Errorf("parse first_failed_at: %w", err)
	}
	if f.LastFailedAt, err = parseTime(lastFailedAt); err != nil {
		return nil, fmt.Errorf("parse last_failed_at: %w", err)
	}
	if f.ParkedAt, err = parseTimePtr(parkedAt); err != nil {
		return nil, fmt.Errorf("parse parked_at: %w", err)
	}
	if f.NextRetryAt, err = parseTimePtr(nextRetryAt); err != nil {
		return nil, fmt.Errorf("parse next_retry_at: %w", err)
	}
	if f.NotifiedAt, err = parseTimePtr(notifiedAt); err != nil {
		return nil, fmt.Errorf("parse notified_at: %w", err)
	}
	return &f, nil
}
