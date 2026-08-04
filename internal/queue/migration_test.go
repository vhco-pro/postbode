package queue_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 4, Criterion: "Migration runner so the schema can evolve across phases 12 and 14 without a wipe"
func TestMigrationRunnerIsIdempotentAndPreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")

	db1, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	stageMessage(t, db1, ctx, "msg-1")
	id := stageItem(t, db1, ctx, "msg-1", "hash-a")
	if err := db1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen against the same file — the migration runner must not error
	// on already-applied migrations, and must not wipe or duplicate data.
	db2, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open (reapplying migrations): %v", err)
	}
	defer func() { _ = db2.Close() }()

	item, err := db2.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem after reopen: %v", err)
	}
	if item.SHA256 != "hash-a" {
		t.Errorf("item.SHA256 = %q after reopen, want %q", item.SHA256, "hash-a")
	}

	// Open a third time for good measure: this must remain a no-op against
	// an up-to-date schema.
	db3, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("third Open: %v", err)
	}
	defer func() { _ = db3.Close() }()
}
