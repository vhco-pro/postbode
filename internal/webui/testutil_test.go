package webui_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// testToken is a fixed, valid, non-empty session token used across this
// package's tests wherever the exact value doesn't matter.
const testToken = "test-session-token-0123456789"

// openTestDB opens a fresh queue database in a per-test temp directory and
// returns both the handle and its on-disk path (needed by tests that must
// reach into the schema directly for state internal/queue's own API
// cannot yet produce — see setDuplicateFlags/setTeachingFlags below).
func openTestDB(t *testing.T) (*queue.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := queue.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// stageTestItem records a message and stages one item for it, returning
// the item id. spoolPath, when non-empty, is written as the item's
// spool_path column (tests that exercise /preview create the actual file
// on disk themselves).
func stageTestItem(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID, from, subject, spoolPath, identityKey string) int64 {
	t.Helper()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: gmailMessageID,
		From:           from,
		Subject:        subject,
	}); err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   gmailMessageID,
		SpoolPath:        spoolPath,
		OrigFilename:     "invoice.pdf",
		ProposedFilename: "2026-08-04-vendor-invoice.pdf",
		MimeType:         "application/pdf",
		SizeBytes:        1024,
		SHA256:           gmailMessageID + "-hash",
		IdentityKey:      identityKey,
	})
	if err != nil {
		t.Fatalf("StageItem(%s): %v", gmailMessageID, err)
	}
	if res.Skipped || res.Status != queue.StatusStaged {
		t.Fatalf("StageItem(%s): unexpected result %+v", gmailMessageID, res)
	}
	return res.ItemID
}

// setDuplicateFlags reaches past internal/queue's public API to set
// possible_duplicate/linked_item_id directly on itemID, simulating the
// state Phase 12's L3 matching engine will eventually produce. This phase
// only builds the UI's *rendering* of that state (F-47), not the engine
// that sets it, so tests that exercise rendering must manufacture it by
// hand — a raw UPDATE against the same schema internal/queue owns, kept to
// this one narrow helper rather than duplicated across every test.
func setDuplicateFlags(t *testing.T, dbPath string, itemID, linkedItemID int64) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE item SET possible_duplicate = 1, linked_item_id = ? WHERE id = ?`, linkedItemID, itemID); err != nil {
		t.Fatalf("set possible_duplicate on item %d: %v", itemID, err)
	}
}

// setProbablyAlreadyHandled is setDuplicateFlags's counterpart for F-35's
// probably_already_handled flag.
func setProbablyAlreadyHandled(t *testing.T, dbPath string, itemID int64) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE item SET probably_already_handled = 1 WHERE id = ?`, itemID); err != nil {
		t.Fatalf("set probably_already_handled on item %d: %v", itemID, err)
	}
}
