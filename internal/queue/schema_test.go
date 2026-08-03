package queue_test

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Schema for `message`, `item`, `vendor_teaching`, `sync_state`, `decision_log` exactly as spec §5.2, on `modernc.org/sqlite` with WAL, no cgo (F-40, NF-01)"
func TestSchemaCreatesAllFiveEntities(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Verify each table exists and is queryable with zero rows on a fresh DB.
	raw := rawConn(t, db)
	for _, table := range []string{"message", "item", "vendor_teaching", "sync_state", "decision_log"} {
		var count int
		if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("table %q does not exist or is not queryable: %v", table, err)
		}
		if count != 0 {
			t.Errorf("table %q: fresh database has %d rows, want 0", table, count)
		}
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Schema for `message`, `item`, `vendor_teaching`, `sync_state`, `decision_log` exactly as spec §5.2, on `modernc.org/sqlite` with WAL, no cgo (F-40, NF-01)"
func TestSchemaRunsInWALMode(t *testing.T) {
	db := openTestDB(t)
	raw := rawConn(t, db)

	var mode string
	if err := raw.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want %q", mode, "wal")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Constraints: unique index on `message.gmail_message_id`; unique `item.uuid` when non-null; partial unique index on `item.sha256` for the active status set"
func TestConstraints(t *testing.T) {
	t.Run("unique index on message.gmail_message_id", func(t *testing.T) {
		db := openTestDB(t)
		ctx := context.Background()
		stageMessage(t, db, ctx, "msg-dup")

		seen, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-dup", From: "other@example.com"})
		if err != nil {
			t.Fatalf("RecordMessageIfNew (second call): %v", err)
		}
		if !seen {
			t.Fatal("RecordMessageIfNew: second insert of the same gmail_message_id was not reported as already seen")
		}

		// Prove uniqueness is enforced at the SQL level too, not only by
		// the ON CONFLICT DO NOTHING clause our own API uses.
		raw := rawConn(t, db)
		_, err = raw.ExecContext(ctx, `INSERT INTO message (gmail_message_id, first_seen_at) VALUES (?, ?)`, "msg-dup", "2026-01-01T00:00:00Z")
		if err == nil {
			t.Fatal("raw INSERT of a duplicate gmail_message_id succeeded; want a UNIQUE constraint violation")
		}
	})

	t.Run("unique item.uuid when non-null", func(t *testing.T) {
		db := openTestDB(t)
		ctx := context.Background()
		stageMessage(t, db, ctx, "msg-1")
		id1 := stageItem(t, db, ctx, "msg-1", "hash-a")
		if err := db.Approve(ctx, id1, queue.ActorHuman); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := db.MarkUploaded(ctx, id1, "uuid-123", 1); err != nil {
			t.Fatalf("MarkUploaded: %v", err)
		}

		id2 := stageItem(t, db, ctx, "msg-1", "hash-b")
		if err := db.Approve(ctx, id2, queue.ActorHuman); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := db.MarkUploaded(ctx, id2, "uuid-123", 1); err == nil {
			t.Fatal("MarkUploaded: assigning a uuid already used by another item succeeded; want a UNIQUE constraint violation")
		}

		// Multiple NULL uuids must coexist — the constraint is scoped to
		// non-null values only.
		raw := rawConn(t, db)
		var nullCount int
		if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM item WHERE uuid IS NULL`).Scan(&nullCount); err != nil {
			t.Fatalf("count NULL uuids: %v", err)
		}
		if nullCount == 0 {
			t.Fatal("expected at least one item with a NULL uuid to exist alongside the uploaded one")
		}
	})

	t.Run("partial unique index on item.sha256 for the active status set", func(t *testing.T) {
		db := openTestDB(t)
		ctx := context.Background()
		stageMessage(t, db, ctx, "msg-1")
		stageItem(t, db, ctx, "msg-1", "same-hash")

		raw := rawConn(t, db)
		// A second row with the same hash and an active status must be
		// rejected at the SQL level.
		for _, status := range []queue.Status{queue.StatusStaged, queue.StatusApproved, queue.StatusUploaded, queue.StatusAlreadyInPortal} {
			_, err := raw.ExecContext(ctx, `
				INSERT INTO item (gmail_message_id, sha256, status, staged_at) VALUES (?, ?, ?, ?)
			`, "msg-1", "same-hash", string(status), "2026-01-01T00:00:00Z")
			if err == nil {
				t.Errorf("raw INSERT of a second item with sha256=%q and status=%q succeeded; want a UNIQUE constraint violation", "same-hash", status)
			}
		}

		// But a row outside the active set — duplicate_linked — is
		// explicitly allowed to share the hash (see the dedup_l2 tests for
		// the full StageItem-level behaviour).
		if _, err := raw.ExecContext(ctx, `
			INSERT INTO item (gmail_message_id, sha256, status, staged_at) VALUES (?, ?, ?, ?)
		`, "msg-1", "same-hash", string(queue.StatusDuplicateLinked), "2026-01-01T00:00:00Z"); err != nil {
			t.Errorf("raw INSERT of a duplicate_linked item with a hash already used by an active item failed: %v", err)
		}
	})
}

// rawConn opens an independent, raw *sql.DB connection to the same file the
// given queue.DB was opened against, for tests that need to introspect
// sqlite_master or attempt writes the package API deliberately disallows.
// queue.DB restricts itself to a single connection, so this connection is
// used read/write directly rather than borrowed from the package.
func rawConn(t *testing.T, db *queue.DB) *sql.DB {
	t.Helper()
	path := dbPath(t, db)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw connection: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw
}
