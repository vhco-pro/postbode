package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StageItem inserts one extracted document as a queue item, applying — in
// order, inside a single transaction — the F-44 rejection memory check and
// the F-31 (L2) SHA-256 dedup check:
//
//  1. If (gmail_message_id, sha256) was previously rejected, nothing is
//     inserted at all: StageResult.Skipped is true (F-44).
//  2. Otherwise, if sha256 matches an item currently in one of the active
//     statuses (staged/approved/uploaded/already_in_portal), the new item is
//     inserted with status=duplicate_linked, dedup_layer=L2 and
//     linked_item_id pointing at the match. It is never uploadable (F-31,
//     AC-11).
//  3. Otherwise the item is inserted as status=staged.
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

	// F-31 (L2): SHA-256 match against the active status set.
	var linkedID int64
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM item WHERE sha256 = ? AND status IN ('staged','approved','uploaded','already_in_portal')
		ORDER BY id LIMIT 1
	`, in.SHA256).Scan(&linkedID)

	status := StatusStaged
	dedupLayer := DedupLayer("")
	var linkedItemID *int64
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

	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO item (
			gmail_message_id, spool_path, orig_filename, proposed_filename, mime_type,
			size_bytes, sha256, identity_key, identity_confidence, identity_source,
			status, needs_manual_handling, low_confidence, unsupported_type,
			linked_item_id, dedup_layer, staged_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		in.GmailMessageID, stringOrNil(in.SpoolPath), stringOrNil(in.OrigFilename), stringOrNil(in.ProposedFilename), stringOrNil(in.MimeType),
		in.SizeBytes, in.SHA256, stringOrNil(in.IdentityKey), stringOrNil(in.IdentityConfidence), stringOrNil(in.IdentitySource),
		string(status), boolToInt(in.NeedsManualHandling), boolToInt(in.LowConfidence), boolToInt(in.UnsupportedType),
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
	defer rows.Close()

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

// ItemsByMessageID returns every item staged for the given gmail_message_id,
// ordered by id.
func (db *DB) ItemsByMessageID(ctx context.Context, gmailMessageID string) ([]*Item, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `SELECT `+itemColumns+` FROM item WHERE gmail_message_id = ? ORDER BY id`, gmailMessageID)
	if err != nil {
		return nil, fmt.Errorf("queue: ItemsByMessageID: %w", err)
	}
	defer rows.Close()

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
	defer rows.Close()

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
