package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ClaimApproved is the F-53 foundation: it selects the oldest unclaimed
// approved item and marks it claimed, inside a single transaction. Because
// *DB restricts the pool to one connection (see Open), a second concurrent
// call — from another goroutine, or from a second uploader process racing
// on the same file — cannot observe or claim the same row: database/sql
// serializes access to the sole connection, so the two transactions run one
// after the other, and the loser's SELECT (issued only once it finally gets
// the connection) sees claimed_at already set and returns nil.
//
// ClaimApproved returns (nil, nil) when there is nothing claimable — that
// is not an error condition.
//
// Full crash-safe retry semantics (releasing a stale claim after a process
// death mid-upload, F-52) are an uploader (Phase 9) concern; this method
// only provides the atomic select-and-mark primitive Phase 9 builds on.
func (db *DB) ClaimApproved(ctx context.Context) (*Item, error) {
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("queue: ClaimApproved: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM item WHERE status = ? AND claimed_at IS NULL ORDER BY staged_at, id LIMIT 1
	`, string(StatusApproved)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("queue: ClaimApproved: commit (no rows): %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: ClaimApproved: select: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE item SET claimed_at = ? WHERE id = ?`, timeToDB(time.Now().UTC()), id); err != nil {
		return nil, fmt.Errorf("queue: ClaimApproved: claim item %d: %w", id, err)
	}

	row := tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM item WHERE id = ?`, id)
	item, err := scanItem(row)
	if err != nil {
		return nil, fmt.Errorf("queue: ClaimApproved: read claimed item %d: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("queue: ClaimApproved: commit: %w", err)
	}
	return item, nil
}

// ClaimApprovedDue is ClaimApproved, plus a Phase 9 F-51 backoff filter: an
// item whose next_retry_at is in the future (a retryable failure scheduled a
// later attempt) is not claimable until now has reached it. An item that has
// never failed (next_retry_at IS NULL) is claimable immediately, exactly
// like ClaimApproved. Same one-transaction select-and-mark shape, so the
// F-53 atomicity guarantee (a concurrent/restarted uploader cannot double-
// claim) holds identically.
func (db *DB) ClaimApprovedDue(ctx context.Context, now time.Time) (*Item, error) {
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("queue: ClaimApprovedDue: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM item
		WHERE status = ? AND claimed_at IS NULL AND (next_retry_at IS NULL OR next_retry_at <= ?)
		ORDER BY staged_at, id LIMIT 1
	`, string(StatusApproved), timeToDB(now.UTC())).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("queue: ClaimApprovedDue: commit (no rows): %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("queue: ClaimApprovedDue: select: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE item SET claimed_at = ? WHERE id = ?`, timeToDB(now.UTC()), id); err != nil {
		return nil, fmt.Errorf("queue: ClaimApprovedDue: claim item %d: %w", id, err)
	}

	row := tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM item WHERE id = ?`, id)
	item, err := scanItem(row)
	if err != nil {
		return nil, fmt.Errorf("queue: ClaimApprovedDue: read claimed item %d: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("queue: ClaimApprovedDue: commit: %w", err)
	}
	return item, nil
}

// ReleaseClaim clears claimed_at without changing status, making an item
// claimable again. Provided as the Phase 9 uploader's escape hatch for a
// retryable failure (item stays approved, next poll can claim it again);
// not exercised by the F-41 lifecycle transitions themselves.
func (db *DB) ReleaseClaim(ctx context.Context, itemID int64) error {
	if _, err := db.sqlDB.ExecContext(ctx, `UPDATE item SET claimed_at = NULL WHERE id = ?`, itemID); err != nil {
		return fmt.Errorf("queue: ReleaseClaim(%d): %w", itemID, err)
	}
	return nil
}

// ReleaseStaleClaims clears claimed_at on every approved item whose claim is
// older than olderThan (relative to now) — F-52's answer to a process that
// claimed an item and then died before ever reaching MarkUploaded/MarkFailed
// (a crash strictly between "claimed" and "outcome recorded", a narrower
// window than "approved and upload" but a real one: ClaimApproved's claim
// commits durably before any HTTP call is made). A daemon calls this once at
// startup (and/or on a recurring tick) before running the uploader, so a
// claim abandoned by a killed process does not strand the item forever —
// restoring it to normal ClaimApprovedDue eligibility with no re-approval
// required (AC-18). Returns the number of claims released.
func (db *DB) ReleaseStaleClaims(ctx context.Context, now time.Time, olderThan time.Duration) (int64, error) {
	cutoff := now.Add(-olderThan)
	res, err := db.sqlDB.ExecContext(ctx, `
		UPDATE item SET claimed_at = NULL
		WHERE status = ? AND claimed_at IS NOT NULL AND claimed_at <= ?
	`, string(StatusApproved), timeToDB(cutoff.UTC()))
	if err != nil {
		return 0, fmt.Errorf("queue: ReleaseStaleClaims: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("queue: ReleaseStaleClaims: rows affected: %w", err)
	}
	return n, nil
}
