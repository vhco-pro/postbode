package cli_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

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

func stageMessage(t *testing.T, db *queue.DB, gmailMessageID, from, subject string) {
	t.Helper()
	if _, err := db.RecordMessageIfNew(context.Background(), queue.Message{
		GmailMessageID: gmailMessageID,
		From:           from,
		Subject:        subject,
	}); err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}
}

func stageItem(t *testing.T, db *queue.DB, gmailMessageID, sha256, filename string) int64 {
	t.Helper()
	res, err := db.StageItem(context.Background(), queue.NewItem{
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
