package queue

// White-box (package queue, not queue_test) crash-safety tests that need
// direct access to the unexported sqlDB handle to simulate a transaction
// that never commits — standing in for a process kill mid-write. Every
// other test file in this package exercises the public API only.

import (
	"context"
	"path/filepath"
	"testing"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Schema for `message`, `item`, `vendor_teaching`, `sync_state`, `decision_log` exactly as spec §5.2, on `modernc.org/sqlite` with WAL, no cgo (F-40, NF-01)"
//
// WAL mode is what makes this package's crash-safety story work: an
// uncommitted transaction's writes are never visible to another connection
// against the same file, and a process death before COMMIT leaves the
// database exactly as if the write had never been attempted.
func TestKillAndReopenLeavesNoHalfCommittedTransaction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if _, err := db.RecordMessageIfNew(ctx, Message{GmailMessageID: "msg-1", From: "a@b.com"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}

	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO item (gmail_message_id, sha256, status, staged_at) VALUES (?, ?, ?, ?)
	`, "msg-1", "never-committed-hash", string(StatusStaged), "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("insert inside uncommitted tx: %v", err)
	}
	// Deliberately do NOT commit and do NOT roll back — this is the
	// simulated crash. db (and its one held connection, and the open tx)
	// is simply abandoned; we never call db.Close(), since that would
	// block waiting for the still-checked-out connection to be returned to
	// the pool, which a real process kill does not have to wait for
	// either (the OS reclaims the file descriptor unconditionally).

	// A second, independent handle standing in for the restarted process.
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after simulated crash: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	var count int
	if err := db2.sqlDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM item WHERE sha256 = ?`, "never-committed-hash").Scan(&count); err != nil {
		t.Fatalf("count after reopen: %v", err)
	}
	if count != 0 {
		t.Fatalf("an item inserted inside a transaction that was never committed survived a simulated crash: count = %d, want 0", count)
	}

	// Clean up the abandoned transaction/connection so this test doesn't
	// leak resources for the rest of the test binary's run.
	_ = tx.Rollback()
	_ = db.sqlDB.Close()
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "`ClaimApproved(ctx)` — select-and-mark inside one transaction so a concurrent or restarted uploader sees zero claimable rows (F-53 foundation, proven in Phase 9)"
//
// An interrupted claim (the transaction never commits) must not leave the
// item half-claimed: no claimed_at, still cleanly claimable by the next
// real ClaimApproved call.
func TestInterruptedClaimLeavesNoHalfState(t *testing.T) {
	ctx := context.Background()
	db := openInternalTestDB(t)

	if _, err := db.RecordMessageIfNew(ctx, Message{GmailMessageID: "msg-1", From: "a@b.com"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	res, err := db.StageItem(ctx, NewItem{GmailMessageID: "msg-1", SHA256: "hash-a"})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}
	if err := db.Approve(ctx, res.ItemID, ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	// Reach into the same code path ClaimApproved uses, but stop short of
	// committing — simulating an interruption between the UPDATE and the
	// COMMIT.
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM item WHERE status = ? AND claimed_at IS NULL ORDER BY staged_at, id LIMIT 1
	`, string(StatusApproved)).Scan(&id); err != nil {
		t.Fatalf("select claimable: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE item SET claimed_at = ? WHERE id = ?`, "2026-01-01T00:00:00Z", id); err != nil {
		t.Fatalf("update claimed_at: %v", err)
	}
	// Interrupted: roll back instead of committing, exactly what a crash
	// before COMMIT achieves for free.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// The real ClaimApproved must still see this item as claimable — the
	// interrupted attempt left no trace.
	item, err := db.ClaimApproved(ctx)
	if err != nil {
		t.Fatalf("ClaimApproved after an interrupted claim attempt: %v", err)
	}
	if item == nil {
		t.Fatal("ClaimApproved returned nil after an interrupted (rolled-back) claim attempt; the item should still be claimable")
	}
	if item.ID != res.ItemID {
		t.Errorf("ClaimApproved claimed item %d, want %d", item.ID, res.ItemID)
	}
}

// openInternalTestDB is testutil_test.go's openTestDB, duplicated here
// because white-box tests (package queue) cannot import the black-box test
// helpers declared in package queue_test.
func openInternalTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
