package queue_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// dbPaths tracks the on-disk path each *queue.DB returned by openTestDB was
// opened against, so tests that need a second, independent raw connection
// to the same file (rawConn, in schema_test.go) can find it. Guarded by a
// mutex since ClaimApproved's concurrency tests open the map from multiple
// goroutines... in practice only openTestDB itself writes to it, but reads
// happen from test goroutines too, so this stays safe either way.
var (
	dbPathsMu sync.Mutex
	dbPaths   = map[*queue.DB]string{}
)

// openTestDB opens a fresh queue database in a per-test temp directory —
// never a shared fixture path, per NF-09/test hygiene.
func openTestDB(t *testing.T) *queue.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := queue.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	dbPathsMu.Lock()
	dbPaths[db] = path
	dbPathsMu.Unlock()
	t.Cleanup(func() {
		_ = db.Close()
		dbPathsMu.Lock()
		delete(dbPaths, db)
		dbPathsMu.Unlock()
	})
	return db
}

// tempDBPath returns a fresh per-test path suitable for queue.Open, without
// opening anything — used by tests that need to open/close/reopen the same
// file explicitly (restart / crash-safety scenarios).
func tempDBPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "queue.db")
}

// dbPath returns the on-disk path a *queue.DB from openTestDB was opened
// against.
func dbPath(t *testing.T, db *queue.DB) string {
	t.Helper()
	dbPathsMu.Lock()
	defer dbPathsMu.Unlock()
	path, ok := dbPaths[db]
	if !ok {
		t.Fatal("dbPath: db was not opened via openTestDB")
	}
	return path
}

// stageMessage records a minimal message row, failing the test on error.
func stageMessage(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID string) {
	t.Helper()
	seen, err := db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: gmailMessageID,
		From:           "vendor@example.com",
		Subject:        "invoice",
	})
	if err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}
	if seen {
		t.Fatalf("RecordMessageIfNew(%s): unexpectedly already seen", gmailMessageID)
	}
}

// stageItem stages one item with the given message id and sha256, failing
// the test on error or an unexpected skip/duplicate.
func stageItem(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID, sha256 string) int64 {
	t.Helper()
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: gmailMessageID,
		SpoolPath:      "/spool/" + sha256 + ".pdf",
		OrigFilename:   "invoice.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      1024,
		SHA256:         sha256,
	})
	if err != nil {
		t.Fatalf("StageItem(%s, %s): %v", gmailMessageID, sha256, err)
	}
	if res.Skipped {
		t.Fatalf("StageItem(%s, %s): unexpectedly skipped: %s", gmailMessageID, sha256, res.SkipReason)
	}
	if res.Status != queue.StatusStaged {
		t.Fatalf("StageItem(%s, %s): status = %s, want staged", gmailMessageID, sha256, res.Status)
	}
	return res.ItemID
}

// stageAndApprove stages a fresh message + item and approves it, returning
// the item id.
func stageAndApprove(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID, sha256 string) int64 {
	t.Helper()
	stageMessage(t, db, ctx, gmailMessageID)
	id := stageItem(t, db, ctx, gmailMessageID, sha256)
	if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
		t.Fatalf("Approve(%d): %v", id, err)
	}
	return id
}
