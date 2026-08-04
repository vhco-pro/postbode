package queue

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // registers the "sqlite" driver, pure Go, no cgo (NF-01)
)

// DB is the durable Postbode review queue store.
type DB struct {
	sqlDB *sql.DB
}

// Open opens (creating if necessary) the SQLite file at path in WAL mode and
// runs any pending migrations. The returned *DB restricts the connection
// pool to a single connection: SQLite allows exactly one writer, and this
// makes every transaction in this package (StageItem, lifecycle
// transitions, ClaimApproved) naturally serialized by database/sql rather
// than racing on SQLITE_BUSY (F-53).
func Open(ctx context.Context, path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("queue: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)

	db := &DB{sqlDB: sqlDB}

	if err := db.applyPragmas(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := db.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) applyPragmas(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.sqlDB.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("queue: apply pragma %q: %w", p, err)
		}
	}
	return nil
}

// Close closes the underlying database handle.
func (db *DB) Close() error {
	return db.sqlDB.Close()
}

// migrate applies every migration in migrations that schema_migrations does
// not yet record as applied, each inside its own transaction so a failure
// partway through one migration cannot leave the schema half-changed.
func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sqlDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		return fmt.Errorf("queue: create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := db.sqlDB.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("queue: read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("queue: scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("queue: iterate schema_migrations: %w", err)
	}

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}
		if err := db.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("queue: apply migration %d: %w", m.version, err)
		}
	}
	return nil
}

func (db *DB) applyMigration(ctx context.Context, m migration) error {
	tx, err := db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range m.stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %q: %w", stmt, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, m.version); err != nil {
		return err
	}
	return tx.Commit()
}
