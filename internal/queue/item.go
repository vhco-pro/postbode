package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StageItem inserts one extracted document as a queue item, applying — in
// order, inside a single transaction — the F-44 rejection memory check,
// the F-36 known-Peppol suppression, the F-31 (L2) SHA-256 dedup check,
// the F-32/F-33 (L3) identity-key match and the F-34/F-35 (L4) vendor
// teaching match:
//
//  1. If (gmail_message_id, sha256) was previously rejected, nothing is
//     inserted at all: StageResult.Skipped is true (F-44).
//  2. Otherwise, if in.SuppressedPeppol is set (the caller already matched
//     the sender against vendors.known_peppol), the item is inserted with
//     status=suppressed_peppol. L2/L3/L4 are skipped — a config-declared
//     Peppol vendor is a stronger, human-authored signal than any
//     heuristic this package could add on top. It still stages: never
//     uploadable without an explicit override, never dropped (F-36,
//     AC-14).
//  3. Otherwise, if sha256 matches an item currently in one of the active
//     statuses (staged/approved/uploaded/already_in_portal), the new item is
//     inserted with status=duplicate_linked, dedup_layer=L2 and
//     linked_item_id pointing at the match. It is never uploadable (F-31,
//     AC-11).
//  4. Otherwise the item is inserted as status=staged, and two independent,
//     non-suppressing annotations run against that same "staged" row:
//     - L3 (F-32/F-33): if in.IdentityKey matches an item in the active
//     status set, possible_duplicate is set, dedup_layer='L3' and
//     linked_item_id points at the match. The item still stages —
//     nothing about this ever changes its status.
//     - L4 (F-34/F-35): if in.VendorDomain has a vendor_teaching row
//     (an earlier "already in portal" action), probably_already_handled
//     is set. Again, the status is untouched.
//
// Both L3 and L4 are annotations, never gates: see CLAUDE.md and
// ADR-001 — "L3 and L4 must NEVER auto-suppress."
func (db *DB) StageItem(ctx context.Context, in NewItem) (StageResult, error) {
	if in.GmailMessageID == "" {
		return StageResult{}, errors.New("queue: StageItem: gmail message id is required")
	}
	if in.SHA256 == "" {
		return StageResult{}, errors.New("queue: StageItem: sha256 is required")
	}

	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return StageResult{}, fmt.Errorf("queue: StageItem: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// F-44: rejection memory. A (gmail_message_id, sha256) pair once
	// rejected never re-stages.
	var rejectedMarker int
	err = tx.QueryRowContext(ctx, `
		SELECT 1 FROM item WHERE gmail_message_id = ? AND sha256 = ? AND status = ? LIMIT 1
	`, in.GmailMessageID, in.SHA256, string(StatusRejected)).Scan(&rejectedMarker)
	switch {
	case err == nil:
		if err := tx.Commit(); err != nil {
			return StageResult{}, fmt.Errorf("queue: StageItem: commit rejection-memory skip: %w", err)
		}
		return StageResult{Skipped: true, SkipReason: "rejected memory (F-44)"}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return StageResult{}, fmt.Errorf("queue: StageItem: check rejection memory: %w", err)
	}

	status := StatusStaged
	dedupLayer := DedupLayer("")
	var linkedItemID *int64
	var possibleDuplicate, probablyAlreadyHandled bool

	if in.SuppressedPeppol {
		// F-36: config-declared suppression takes priority and short-
		// circuits L2/L3/L4 — see the doc comment above.
		status = StatusSuppressedPeppol
	} else {
		// F-31 (L2): SHA-256 match against the active status set.
		var linkedID int64
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM item WHERE sha256 = ? AND status IN ('staged','approved','uploaded','already_in_portal')
			ORDER BY id LIMIT 1
		`, in.SHA256).Scan(&linkedID)

		switch {
		case err == nil:
			status = StatusDuplicateLinked
			dedupLayer = DedupLayerL2
			id := linkedID
			linkedItemID = &id
		case errors.Is(err, sql.ErrNoRows):
			// no match — status stays StatusStaged
		default:
			return StageResult{}, fmt.Errorf("queue: StageItem: check L2 dedup: %w", err)
		}

		if status == StatusStaged {
			// F-32/F-33 (L3): identity-key match, badge only — never
			// changes status, never blocks upload.
			if in.IdentityKey != "" {
				var matchedID int64
				err = tx.QueryRowContext(ctx, `
					SELECT id FROM item WHERE identity_key = ? AND status IN ('staged','approved','uploaded','already_in_portal')
					ORDER BY id LIMIT 1
				`, in.IdentityKey).Scan(&matchedID)
				switch {
				case err == nil:
					possibleDuplicate = true
					dedupLayer = DedupLayerL3
					id := matchedID
					linkedItemID = &id
				case errors.Is(err, sql.ErrNoRows):
					// no match
				default:
					return StageResult{}, fmt.Errorf("queue: StageItem: check L3 dedup: %w", err)
				}
			}

			// F-34/F-35 (L4): vendor-teaching match, badge only — never
			// changes status, never blocks upload.
			if in.VendorDomain != "" {
				var teachingMarker string
				err = tx.QueryRowContext(ctx, `
					SELECT vendor_domain FROM vendor_teaching WHERE vendor_domain = ? LIMIT 1
				`, in.VendorDomain).Scan(&teachingMarker)
				switch {
				case err == nil:
					probablyAlreadyHandled = true
				case errors.Is(err, sql.ErrNoRows):
					// vendor never taught
				default:
					return StageResult{}, fmt.Errorf("queue: StageItem: check L4 vendor teaching: %w", err)
				}
			}
		}
	}

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO item (
			gmail_message_id, spool_path, orig_filename, proposed_filename, mime_type,
			size_bytes, sha256, identity_key, identity_confidence, identity_source,
			status, needs_manual_handling, low_confidence, possible_duplicate,
			probably_already_handled, unsupported_type,
			linked_item_id, dedup_layer, staged_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		in.GmailMessageID, stringOrNil(in.SpoolPath), stringOrNil(in.OrigFilename), stringOrNil(in.ProposedFilename), stringOrNil(in.MimeType),
		in.SizeBytes, in.SHA256, stringOrNil(in.IdentityKey), stringOrNil(in.IdentityConfidence), stringOrNil(in.IdentitySource),
		string(status), boolToInt(in.NeedsManualHandling), boolToInt(in.LowConfidence), boolToInt(possibleDuplicate),
		boolToInt(probablyAlreadyHandled), boolToInt(in.UnsupportedType),
		linkedItemID, stringOrNil(string(dedupLayer)), timeToDB(now),
	)
	if err != nil {
		return StageResult{}, fmt.Errorf("queue: StageItem: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return StageResult{}, fmt.Errorf("queue: StageItem: last insert id: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO item_transition (item_id, from_status, to_status, actor, at) VALUES (?, NULL, ?, ?, ?)
	`, id, string(status), string(ActorDaemon), timeToDB(now)); err != nil {
		return StageResult{}, fmt.Errorf("queue: StageItem: log initial transition: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return StageResult{}, fmt.Errorf("queue: StageItem: commit: %w", err)
	}

	return StageResult{ItemID: id, Status: status, DedupLayer: dedupLayer, LinkedItemID: linkedItemID}, nil
}

// GetItem reads one item by id.
func (db *DB) GetItem(ctx context.Context, id int64) (*Item, error) {
	row := db.sqlDB.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM item WHERE id = ?`, id)
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("queue: GetItem(%d): %w", id, err)
	}
	if err != nil {
		return nil, fmt.Errorf("queue: GetItem(%d): %w", id, err)
	}
	return item, nil
}

// ItemsBySHA256 returns every item currently carrying the given hash,
// ordered by id — used by tests and by the dedup mechanism itself to
// inspect the full picture (original + any duplicate_linked rows).
func (db *DB) ItemsBySHA256(ctx context.Context, sha256 string) ([]*Item, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `SELECT `+itemColumns+` FROM item WHERE sha256 = ? ORDER BY id`, sha256)
	if err != nil {
		return nil, fmt.Errorf("queue: ItemsBySHA256: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: ItemsBySHA256: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: ItemsBySHA256: %w", err)
	}
	return items, nil
}

// ListByStatus returns every item currently in status, oldest-staged first.
// Added for Phase 8 (webui): the review UI's list view is exactly "every
// item in status=staged", and "approve all" re-derives that same set at
// request time rather than trusting a possibly-stale page (spec §8).
func (db *DB) ListByStatus(ctx context.Context, status Status) ([]*Item, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `SELECT `+itemColumns+` FROM item WHERE status = ? ORDER BY staged_at, id`, string(status))
	if err != nil {
		return nil, fmt.Errorf("queue: ListByStatus(%s): %w", status, err)
	}
	defer func() { _ = rows.Close() }()

	var items []*Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: ListByStatus(%s): scan: %w", status, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: ListByStatus(%s): %w", status, err)
	}
	return items, nil
}

// ListReviewable returns every item currently needing human attention in
// the review UI: status=staged (needs an initial decision) and
// status=suppressed_peppol (needs an explicit F-36/AC-14 override before
// it can even reach the ordinary staged flow) — both stay visible until a
// human acts, oldest first.
func (db *DB) ListReviewable(ctx context.Context) ([]*Item, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `
		SELECT `+itemColumns+` FROM item WHERE status IN (?, ?) ORDER BY staged_at, id
	`, string(StatusStaged), string(StatusSuppressedPeppol))
	if err != nil {
		return nil, fmt.Errorf("queue: ListReviewable: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: ListReviewable: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: ListReviewable: %w", err)
	}
	return items, nil
}

// ListUploadedOlderThan returns every item in status=uploaded whose
// uploaded_at is at or before cutoff, oldest first. Added for Phase 9's
// spool pruning (F-24): the uploader (or the daemon tick that drives it)
// uses this to fetch the candidate set for extract.PruneUploadedItems
// without scanning every item in the database.
func (db *DB) ListUploadedOlderThan(ctx context.Context, cutoff time.Time) ([]*Item, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `
		SELECT `+itemColumns+` FROM item
		WHERE status = ? AND uploaded_at IS NOT NULL AND uploaded_at <= ?
		ORDER BY uploaded_at, id
	`, string(StatusUploaded), timeToDB(cutoff.UTC()))
	if err != nil {
		return nil, fmt.Errorf("queue: ListUploadedOlderThan: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: ListUploadedOlderThan: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: ListUploadedOlderThan: %w", err)
	}
	return items, nil
}

// ItemsByMessageID returns every item staged for the given gmail_message_id,
// ordered by id.
func (db *DB) ItemsByMessageID(ctx context.Context, gmailMessageID string) ([]*Item, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `SELECT `+itemColumns+` FROM item WHERE gmail_message_id = ? ORDER BY id`, gmailMessageID)
	if err != nil {
		return nil, fmt.Errorf("queue: ItemsByMessageID: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []*Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, fmt.Errorf("queue: ItemsByMessageID: scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: ItemsByMessageID: %w", err)
	}
	return items, nil
}

// Transitions returns the full item_transition audit log for one item,
// oldest first.
func (db *DB) Transitions(ctx context.Context, itemID int64) ([]Transition, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `
		SELECT id, item_id, from_status, to_status, actor, at
		FROM item_transition WHERE item_id = ? ORDER BY id
	`, itemID)
	if err != nil {
		return nil, fmt.Errorf("queue: Transitions(%d): %w", itemID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Transition
	for rows.Next() {
		var (
			tr         Transition
			fromStatus sql.NullString
			at         sql.NullString
		)
		if err := rows.Scan(&tr.ID, &tr.ItemID, &fromStatus, &tr.To, &tr.Actor, &at); err != nil {
			return nil, fmt.Errorf("queue: Transitions(%d): scan: %w", itemID, err)
		}
		tr.From = Status(fromStatus.String)
		t, err := parseTime(at)
		if err != nil {
			return nil, fmt.Errorf("queue: Transitions(%d): parse at: %w", itemID, err)
		}
		tr.At = t
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: Transitions(%d): %w", itemID, err)
	}
	return out, nil
}
