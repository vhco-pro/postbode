package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/queue"
)

// withTempHome points userHomeDir at a fresh per-test temp directory for
// the duration of the test, restoring the original afterward. Every
// status/log/review test in this package must use this — run() must never
// touch the real home directory (NF-09-adjacent test hygiene).
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	orig := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = orig })
	return home
}

// openQueueAt opens (and registers cleanup for) the queue database at
// Postbode's default location under home — the same path runStatus/runLog
// resolve via cli.DefaultDBPath — so a test can seed data before invoking
// run() and read it back afterward.
func openQueueAt(t *testing.T, home string) *queue.DB {
	t.Helper()
	db, err := openTestDB(home)
	if err != nil {
		t.Fatalf("open queue db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func openTestDB(home string) (*queue.DB, error) {
	return cli.OpenDB(context.Background(), cli.DefaultDBPath(home))
}

// stageMessage records a minimal message row, failing the test on error.
func stageMessage(t *testing.T, db *queue.DB, gmailMessageID, from, subject string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: gmailMessageID,
		From:           from,
		Subject:        subject,
	}); err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}
}

// stageItem stages one item with the given message id, sha256 and
// filename, failing the test on error or an unexpected skip/duplicate.
func stageItem(t *testing.T, db *queue.DB, gmailMessageID, sha256, filename string) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   gmailMessageID,
		SpoolPath:        "/spool/" + sha256 + ".pdf",
		OrigFilename:     filename,
		ProposedFilename: filename,
		MimeType:         "application/pdf",
		SizeBytes:        1024,
		SHA256:           sha256,
	})
	if err != nil {
		t.Fatalf("StageItem(%s, %s): %v", gmailMessageID, sha256, err)
	}
	if res.Skipped {
		t.Fatalf("StageItem(%s, %s): unexpectedly skipped: %s", gmailMessageID, sha256, res.SkipReason)
	}
	return res.ItemID
}

// stageApproveUpload stages, approves and uploads one item in a single
// call, returning its id. verifiedAt is applied only when non-zero.
func stageApproveUpload(t *testing.T, db *queue.DB, gmailMessageID, sha256, filename, uuid string, verifiedAt time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	stageMessage(t, db, gmailMessageID, "vendor@example.com", "invoice")
	id := stageItem(t, db, gmailMessageID, sha256, filename)
	if err := db.Approve(ctx, id, queue.ActorHuman); err != nil {
		t.Fatalf("Approve(%d): %v", id, err)
	}
	if err := db.MarkUploaded(ctx, id, uuid, 1); err != nil {
		t.Fatalf("MarkUploaded(%d): %v", id, err)
	}
	if !verifiedAt.IsZero() {
		if err := db.MarkVerified(ctx, id, verifiedAt); err != nil {
			t.Fatalf("MarkVerified(%d): %v", id, err)
		}
	}
	return id
}

// backdateStagedAt rewrites item.staged_at directly via a second raw
// connection to the same database file, since no exported queue.DB method
// lets a caller set staged_at to anything but "now". This exists solely so
// tests can exercise the "stuck > 48h" boundary without waiting 48h; the
// "sqlite" driver is already registered process-wide by internal/queue's
// blank import.
func backdateStagedAt(t *testing.T, home string, itemID int64, at time.Time) {
	t.Helper()
	raw, err := sql.Open("sqlite", cli.DefaultDBPath(home))
	if err != nil {
		t.Fatalf("backdateStagedAt: open: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`UPDATE item SET staged_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339Nano), itemID); err != nil {
		t.Fatalf("backdateStagedAt: update: %v", err)
	}
}
