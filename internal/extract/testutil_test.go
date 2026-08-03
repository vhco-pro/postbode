package extract_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// fixtureDir is the repo-root testdata/ directory shared across phases
// (plan §File Reference Summary), reached relative to this package's
// directory since `go test` runs with the package dir as its cwd.
const fixtureDir = "../../testdata"

// loadFixture reads one testdata/*.eml fixture, failing the test if it is
// missing — every fixture referenced by a test must actually exist in the
// corpus.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("loadFixture(%s): %v", name, err)
	}
	return data
}

// openTestDB opens a fresh queue database in a per-test temp directory —
// never a shared fixture path (NF-09 / test hygiene).
func openTestDB(t *testing.T) *queue.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := queue.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// spoolDir returns a fresh per-test spool directory — never a real home
// directory (F-24: the spool base is injectable specifically so tests
// never touch ~/Library/Application Support).
func spoolDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "spool")
}
