package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSyncState reads the singleton sync_state row (spec §5.2), owned by
// gmailwatch (Phase 7). A row that has never been written is not an error:
// GetSyncState returns the zero-value SyncState, which is exactly how a
// fresh daemon distinguishes "first ever poll" (empty HistoryID, F-13's
// windowed fallback) from "history sync is warm" (F-12's incremental path).
func (db *DB) GetSyncState(ctx context.Context) (SyncState, error) {
	row := db.sqlDB.QueryRowContext(ctx, `
		SELECT history_id, last_poll_at, label_id_submitted, token_issued_at, last_auth_error,
		       consecutive_poll_failures, first_poll_failure_at, last_poll_error, poll_stall_notified_at
		FROM sync_state WHERE id = 1
	`)

	var (
		historyID, labelID, lastAuthError     sql.NullString
		lastPollAt, tokenIssuedAt             sql.NullString
		firstPollFailureAt, pollStallNotified sql.NullString
		lastPollError                         sql.NullString
		consecutivePollFailures               sql.NullInt64
	)
	err := row.Scan(&historyID, &lastPollAt, &labelID, &tokenIssuedAt, &lastAuthError,
		&consecutivePollFailures, &firstPollFailureAt, &lastPollError, &pollStallNotified)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SyncState{}, nil
	case err != nil:
		return SyncState{}, fmt.Errorf("queue: GetSyncState: %w", err)
	}

	st := SyncState{
		HistoryID:               historyID.String,
		LabelIDSubmitted:        labelID.String,
		LastAuthError:           lastAuthError.String,
		ConsecutivePollFailures: int(consecutivePollFailures.Int64),
		LastPollError:           lastPollError.String,
	}
	if st.LastPollAt, err = parseTimePtr(lastPollAt); err != nil {
		return SyncState{}, fmt.Errorf("queue: GetSyncState: parse last_poll_at: %w", err)
	}
	if st.TokenIssuedAt, err = parseTimePtr(tokenIssuedAt); err != nil {
		return SyncState{}, fmt.Errorf("queue: GetSyncState: parse token_issued_at: %w", err)
	}
	if st.FirstPollFailureAt, err = parseTimePtr(firstPollFailureAt); err != nil {
		return SyncState{}, fmt.Errorf("queue: GetSyncState: parse first_poll_failure_at: %w", err)
	}
	if st.PollStallNotifiedAt, err = parseTimePtr(pollStallNotified); err != nil {
		return SyncState{}, fmt.Errorf("queue: GetSyncState: parse poll_stall_notified_at: %w", err)
	}
	return st, nil
}

// SaveSyncState upserts the singleton sync_state row (F-12, F-16, F-17).
// Postbode runs exactly one gmailwatch poller against a given database, and
// *DB already restricts the connection pool to one connection (see doc.go),
// so a plain read-modify-write via GetSyncState + SaveSyncState needs no
// additional locking.
func (db *DB) SaveSyncState(ctx context.Context, st SyncState) error {
	_, err := db.sqlDB.ExecContext(ctx, `
		INSERT INTO sync_state (id, history_id, last_poll_at, label_id_submitted, token_issued_at, last_auth_error,
		                        consecutive_poll_failures, first_poll_failure_at, last_poll_error, poll_stall_notified_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			history_id                = excluded.history_id,
			last_poll_at              = excluded.last_poll_at,
			label_id_submitted        = excluded.label_id_submitted,
			token_issued_at           = excluded.token_issued_at,
			last_auth_error           = excluded.last_auth_error,
			consecutive_poll_failures = excluded.consecutive_poll_failures,
			first_poll_failure_at     = excluded.first_poll_failure_at,
			last_poll_error           = excluded.last_poll_error,
			poll_stall_notified_at    = excluded.poll_stall_notified_at
	`,
		stringOrNil(st.HistoryID), timePtrToDB(st.LastPollAt), stringOrNil(st.LabelIDSubmitted),
		timePtrToDB(st.TokenIssuedAt), stringOrNil(st.LastAuthError),
		st.ConsecutivePollFailures, timePtrToDB(st.FirstPollFailureAt),
		stringOrNil(st.LastPollError), timePtrToDB(st.PollStallNotifiedAt),
	)
	if err != nil {
		return fmt.Errorf("queue: SaveSyncState: %w", err)
	}
	return nil
}
