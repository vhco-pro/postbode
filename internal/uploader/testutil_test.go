package uploader_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

const testCompanyNumber = "BE0123456789"

// openTestDB opens a fresh queue database in a per-test temp directory.
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

// stageApprovedItemWithFile stages one message + item backed by a real
// on-disk spool file (so os.Open inside processItem succeeds), approves it,
// and returns its id.
func stageApprovedItemWithFile(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID, sha256 string) int64 {
	t.Helper()

	if _, err := db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: gmailMessageID,
		From:           "vendor@example.com",
		Subject:        "invoice",
	}); err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}

	spoolPath := filepath.Join(t.TempDir(), sha256+".pdf")
	if err := os.WriteFile(spoolPath, []byte("%PDF-1.4 fake content "+sha256), 0o600); err != nil {
		t.Fatalf("write spool file: %v", err)
	}

	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   gmailMessageID,
		SpoolPath:        spoolPath,
		OrigFilename:     "invoice.pdf",
		ProposedFilename: "vendor-2026-01-01-invoice.pdf",
		MimeType:         "application/pdf",
		SizeBytes:        1024,
		SHA256:           sha256,
	})
	if err != nil {
		t.Fatalf("StageItem(%s, %s): %v", gmailMessageID, sha256, err)
	}
	if res.Skipped || res.Status != queue.StatusStaged {
		t.Fatalf("StageItem(%s, %s): unexpected result %+v", gmailMessageID, sha256, res)
	}

	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve(%d): %v", res.ItemID, err)
	}
	return res.ItemID
}

// testClock is a manually-advanced clock, letting backoff/give-up tests
// jump forward by minutes/hours instantly instead of sleeping for real
// (NF-09-adjacent test hygiene: no test may block on real wall-clock time
// for hours).
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(start time.Time) *testClock {
	return &testClock{now: start.UTC()}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
