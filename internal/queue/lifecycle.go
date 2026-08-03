package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// allowedTransitions enumerates the F-41 lifecycle graph. Any (from, to)
// pair not listed here is rejected by transition — including transitions
// out of any status not present as a map key at all, which covers every
// terminal status (uploaded, rejected, already_in_portal, failed,
// suppressed_peppol, duplicate_linked): none of them has an outgoing edge.
var allowedTransitions = map[Status][]Status{
	StatusStaged:   {StatusApproved, StatusRejected, StatusAlreadyInPortal},
	StatusApproved: {StatusUploaded, StatusFailed},
}

func isAllowedTransition(from, to Status) bool {
	for _, candidate := range allowedTransitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// InvalidTransitionError is returned when a requested lifecycle transition
// is not permitted by the F-41 graph. The item's status is left completely
// unchanged — the transition is rejected before anything is written, not
// applied and then reported as an error.
type InvalidTransitionError struct {
	ItemID int64
	From   Status
	To     Status
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("queue: item %d: transition %s -> %s is not permitted by the F-41 lifecycle", e.ItemID, e.From, e.To)
}

// transition is the shared, transactional core of every lifecycle method
// below. It reads the current status, validates the requested move against
// allowedTransitions, applies any extra column updates the caller supplies,
// and logs the transition to item_transition — all inside one transaction,
// so an invalid transition or any failure partway through leaves no trace:
// the whole operation rolls back and the item's status is unchanged.
func (db *DB) transition(ctx context.Context, itemID int64, to Status, actor Actor, extra func(ctx context.Context, tx *sql.Tx) error) error {
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("queue: transition: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var from Status
	err = tx.QueryRowContext(ctx, `SELECT status FROM item WHERE id = ?`, itemID).Scan(&from)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("queue: transition: item %d not found", itemID)
	}
	if err != nil {
		return fmt.Errorf("queue: transition: read current status: %w", err)
	}

	if !isAllowedTransition(from, to) {
		return &InvalidTransitionError{ItemID: itemID, From: from, To: to}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE item SET status = ? WHERE id = ?`, string(to), itemID); err != nil {
		return fmt.Errorf("queue: transition: update status: %w", err)
	}

	if extra != nil {
		if err := extra(ctx, tx); err != nil {
			return fmt.Errorf("queue: transition: apply extra columns: %w", err)
		}
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO item_transition (item_id, from_status, to_status, actor, at) VALUES (?, ?, ?, ?, ?)
	`, itemID, string(from), string(to), string(actor), timeToDB(now)); err != nil {
		return fmt.Errorf("queue: transition: log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("queue: transition: commit: %w", err)
	}
	return nil
}

// Approve moves an item staged -> approved. actor is normally ActorHuman —
// there is no auto-approve path in P1 (G-3).
func (db *DB) Approve(ctx context.Context, itemID int64, actor Actor) error {
	return db.transition(ctx, itemID, StatusApproved, actor, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE item SET approved_at = ? WHERE id = ?`, timeToDB(time.Now().UTC()), itemID)
		return err
	})
}

// Reject moves an item staged -> rejected. This is what makes F-44
// rejection memory possible: once here, StageItem will never re-stage the
// same (gmail_message_id, sha256) pair.
func (db *DB) Reject(ctx context.Context, itemID int64, actor Actor) error {
	return db.transition(ctx, itemID, StatusRejected, actor, nil)
}

// MarkAlreadyInPortal moves an item staged -> already_in_portal (the UI's
// third terminal action, F-34). Writing the vendor_teaching row is the
// caller's responsibility (the webui handler, Phase 8) so that it can be
// done atomically with the caller's own transaction if desired; this method
// only performs the status transition.
func (db *DB) MarkAlreadyInPortal(ctx context.Context, itemID int64, actor Actor) error {
	return db.transition(ctx, itemID, StatusAlreadyInPortal, actor, nil)
}

// MarkUploaded moves a claimed item approved -> uploaded, recording the
// returned uuid and amountOfPages (F-37). actor is always ActorDaemon.
func (db *DB) MarkUploaded(ctx context.Context, itemID int64, uuid string, amountOfPages int) error {
	if uuid == "" {
		return errors.New("queue: MarkUploaded: uuid is required")
	}
	return db.transition(ctx, itemID, StatusUploaded, ActorDaemon, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE item SET uploaded_at = ?, uuid = ?, amount_of_pages = ? WHERE id = ?
		`, timeToDB(time.Now().UTC()), uuid, amountOfPages, itemID)
		return err
	})
}

// MarkFailed moves a claimed item approved -> failed, recording the last
// error (F-51). actor is always ActorDaemon.
func (db *DB) MarkFailed(ctx context.Context, itemID int64, lastError string) error {
	return db.transition(ctx, itemID, StatusFailed, ActorDaemon, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE item SET failed_at = ?, last_error = ? WHERE id = ?
		`, timeToDB(time.Now().UTC()), lastError, itemID)
		return err
	})
}
