package queue_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "Exit criteria: make test && go vet ./... green. A fresh database and a database created at schema version 3 both migrate to 5."
//
// Every real installation is a version-3 database — that is what the shipped
// 0.5.0 binary creates. Migrations 4 and 5 are only ever exercised against a
// fresh file by the rest of the suite, which is the one case that does NOT
// occur in production. This builds an actual v3 database, with real rows in
// it, and migrates it forward.
func TestVersion3DatabaseMigratesForwardWithoutLosingData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")

	// Build a genuine v3 database by hand: the v1 tables this test touches,
	// plus the v2 and v3 additive columns, recorded in schema_migrations
	// exactly as applyMigration would have left them.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	stmts := []string{
		// Byte-identical to db.go's own CREATE TABLE, including the default:
		// applyMigration inserts only the version and relies on it.
		`CREATE TABLE schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE message (
			gmail_message_id     TEXT PRIMARY KEY,
			thread_id            TEXT,
			"from"               TEXT,
			subject              TEXT,
			internal_date        TEXT,
			first_seen_at        TEXT NOT NULL,
			all_docs_uploaded_at TEXT,
			labeled_at           TEXT
		)`,
		`CREATE TABLE sync_state (
			id                  INTEGER PRIMARY KEY CHECK (id = 1),
			history_id          TEXT,
			last_poll_at        TEXT,
			label_id_submitted  TEXT,
			token_issued_at     TEXT
		)`,
		`ALTER TABLE sync_state ADD COLUMN last_auth_error TEXT`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-08-01T00:00:00Z')`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (2, '2026-08-01T00:00:00Z')`,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (3, '2026-08-01T00:00:00Z')`,
		`INSERT INTO message (gmail_message_id, first_seen_at, "from", subject)
			VALUES ('pre-existing-msg', '2026-08-01T00:00:00Z', 'billing@vendor.example', 'Old invoice')`,
		`INSERT INTO sync_state (id, history_id) VALUES (1, '999')`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("building v3 database: %v\n%s", err, s)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// The real migration runner, on a real v3 file.
	db, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("queue.Open on a v3 database: %v", err)
	}
	defer func() { _ = db.Close() }()

	// Pre-existing data survives.
	seen, err := db.MessageSeen(ctx, "pre-existing-msg")
	if err != nil {
		t.Fatalf("MessageSeen: %v", err)
	}
	if !seen {
		t.Error("a message recorded before the migration was lost")
	}

	// The v2/v3-era sync_state row survives, and reads back through the new
	// column list rather than erroring on the added columns.
	st, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState after migration: %v", err)
	}
	if st.HistoryID != "999" {
		t.Errorf("history_id = %q after migration, want it preserved at %q", st.HistoryID, "999")
	}
	// The new columns default sanely on an upgraded row: an existing
	// installation must not come back reporting a phantom stall.
	if !st.PollHealthy() {
		t.Errorf("an upgraded database reports %d consecutive poll failures; a migration must not invent a stall", st.ConsecutivePollFailures)
	}

	// And the new table works, including for a message that has no `message`
	// row — the no-foreign-key case ADR-004 turns on.
	if _, parked, err := db.RecordMessageFailure(ctx, "never-recorded-msg", "500", 1, time.Now().UTC(), time.Hour); err != nil || !parked {
		t.Fatalf("RecordMessageFailure on a migrated database: parked=%v err=%v", parked, err)
	}
	if got, err := db.ListParkedMessages(ctx); err != nil || len(got) != 1 {
		t.Fatalf("ListParkedMessages = %v (err %v), want one entry", got, err)
	}
}
