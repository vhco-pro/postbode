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

// MarkAlreadyInPortalWithTeaching is MarkAlreadyInPortal plus the F-34/F-35
// vendor_teaching write the doc comment above anticipated, done inside the
// same transaction as the status transition (AC-13: "sets status
// already_in_portal, writes a vendor_teaching row" — a crash between two
// separate calls could otherwise leave one written without the other).
// teaching.VendorDomain is required (it is vendor_teaching's primary key);
// an existing row for the same domain is overwritten (upsert), since a
// human re-teaching the same vendor should win over a stale row.
// teaching.MarkedAt defaults to time.Now().UTC() when zero. Performs zero
// upload calls — it only writes local rows (F-34).
func (db *DB) MarkAlreadyInPortalWithTeaching(ctx context.Context, itemID int64, actor Actor, teaching VendorTeaching) error {
	if teaching.VendorDomain == "" {
		return errors.New("queue: MarkAlreadyInPortalWithTeaching: vendor domain is required")
	}
	markedAt := teaching.MarkedAt
	if markedAt.IsZero() {
		markedAt = time.Now().UTC()
	}
	return db.transition(ctx, itemID, StatusAlreadyInPortal, actor, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO vendor_teaching (vendor_domain, identity_key, reason, marked_at, note)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(vendor_domain) DO UPDATE SET
				identity_key = excluded.identity_key,
				reason       = excluded.reason,
				marked_at    = excluded.marked_at,
				note         = excluded.note
		`, teaching.VendorDomain, stringOrNil(teaching.IdentityKey), teaching.Reason, timeToDB(markedAt), stringOrNil(teaching.Note))
		return err
	})
}

// GetVendorTeachingByIdentityKey looks up a vendor_teaching row by the
// identity_key an "already in portal" action recorded it under — the join
// key already available on Item today, used by the webui (Phase 8) to
// render the F-35 "probably already handled" badge's reason and teaching
// date. Returns sql.ErrNoRows (wrapped) when identityKey is empty or no
// row matches. The L4 matching engine that sets an item's
// probably_already_handled flag at staging time is Phase 12's job; this
// method only supports rendering once that flag is set.
func (db *DB) GetVendorTeachingByIdentityKey(ctx context.Context, identityKey string) (*VendorTeaching, error) {
	if identityKey == "" {
		return nil, fmt.Errorf("queue: GetVendorTeachingByIdentityKey(%q): %w", identityKey, sql.ErrNoRows)
	}
	row := db.sqlDB.QueryRowContext(ctx, `
		SELECT vendor_domain, identity_key, reason, marked_at, note
		FROM vendor_teaching WHERE identity_key = ? LIMIT 1
	`, identityKey)

	var (
		vt                   VendorTeaching
		identityKeyCol, note sql.NullString
		markedAt             string
	)
	if err := row.Scan(&vt.VendorDomain, &identityKeyCol, &vt.Reason, &markedAt, &note); err != nil {
		return nil, fmt.Errorf("queue: GetVendorTeachingByIdentityKey(%q): %w", identityKey, err)
	}
	vt.IdentityKey = identityKeyCol.String
	vt.Note = note.String
	t, err := time.Parse(timeLayout, markedAt)
	if err != nil {
		return nil, fmt.Errorf("queue: GetVendorTeachingByIdentityKey(%q): parse marked_at: %w", identityKey, err)
	}
	vt.MarkedAt = t.UTC()
	return &vt, nil
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
