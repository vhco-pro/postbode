package extract_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Fail-safe on disk-full: no queue row committed, message-id **not** marked seen, error logged, retried next poll (spec §8)"
//
// This is the crux ordering the phase brief calls out: a message marked
// seen with no queue row would be a permanently lost invoice (violates
// G-1), since L1 (queue.DB.RecordMessageIfNew) guarantees a seen message
// is NEVER re-extracted. The test proves the ordering directly by
// injecting a Spooler whose every Write call fails — simulating a full
// disk without touching the real filesystem — and then asserting, from
// the queue's own public API, that:
//  1. ExtractMessage returns a non-nil error (so the caller logs it and
//     retries next poll rather than treating this as success).
//  2. Zero queue item rows exist for the message.
//  3. The message id is NOT recorded as seen (MessageSeen reports false),
//     so a subsequent poll will walk this exact message again rather than
//     silently skipping it forever.
func TestExtractMessageDiskFullCommitsNoRowsAndLeavesMessageUnseen(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	diskFullErr := errors.New("simulated disk full: no space left on device")
	failingSpooler := extract.NewFailingSpooler(spoolDir(t), diskFullErr)
	ex := extract.NewWithSpooler(failingSpooler, db)

	raw := loadFixture(t, "nested-three-pdfs.eml")
	res, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-disk-full",
		From:           "billing@ovh.com",
		Raw:            raw,
	})

	// 1. The failure must be surfaced as an error, not swallowed.
	if err == nil {
		t.Fatal("ExtractMessage: err = nil, want a non-nil error when every spool write fails")
	}
	if !errors.Is(err, diskFullErr) {
		t.Errorf("ExtractMessage error does not wrap the injected disk-full error: %v", err)
	}
	if len(res.Items) != 0 {
		t.Errorf("Result.Items has %d entries, want 0 on a failed extraction", len(res.Items))
	}

	// 2. No queue row was committed for this message.
	items, ierr := db.ItemsByMessageID(ctx, "msg-disk-full")
	if ierr != nil {
		t.Fatalf("ItemsByMessageID: %v", ierr)
	}
	if len(items) != 0 {
		t.Fatalf("ItemsByMessageID returned %d rows, want 0 — a disk-full extraction must commit no queue rows", len(items))
	}

	// 3. The message id was NOT marked seen — this is the crux. If it had
	// been, L1 would guarantee this message is never re-extracted, and the
	// invoice inside it would be permanently lost with no queue row to
	// show for it.
	seen, serr := db.MessageSeen(ctx, "msg-disk-full")
	if serr != nil {
		t.Fatalf("MessageSeen: %v", serr)
	}
	if seen {
		t.Fatal("MessageSeen = true after a disk-full extraction failure, want false — the message must be retried on the next poll, not silently treated as processed")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 5, Criterion: "Fail-safe on disk-full: no queue row committed, message-id **not** marked seen, error logged, retried next poll (spec §8)"
//
// Extends the single-message case to a partial failure inside a
// multi-document message: the first candidate spools fine, the second
// fails. The whole message must still fail atomically — no partial state
// where one of the three documents got staged and the other two did not.
func TestExtractMessageDiskFullPartwayThroughMultiDocMessageIsAtomic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	callCount := 0
	diskFullErr := errors.New("simulated disk full on the second write")
	sd := spoolDir(t)
	spooler := extract.NewSpoolerForTest(sd, func() error {
		callCount++
		if callCount >= 2 {
			return diskFullErr
		}
		return nil
	})
	ex := extract.NewWithSpooler(spooler, db)

	raw := loadFixture(t, "nested-three-pdfs.eml") // 3 candidate documents
	_, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: "msg-disk-full-partway",
		From:           "billing@ovh.com",
		Raw:            raw,
	})
	if err == nil {
		t.Fatal("ExtractMessage: err = nil, want a non-nil error when the second of three spool writes fails")
	}

	items, ierr := db.ItemsByMessageID(ctx, "msg-disk-full-partway")
	if ierr != nil {
		t.Fatalf("ItemsByMessageID: %v", ierr)
	}
	if len(items) != 0 {
		t.Fatalf("ItemsByMessageID returned %d rows, want 0 — the first document's successful spool write must not leave a queue row behind when the second fails", len(items))
	}

	seen, serr := db.MessageSeen(ctx, "msg-disk-full-partway")
	if serr != nil {
		t.Fatalf("MessageSeen: %v", serr)
	}
	if seen {
		t.Fatal("MessageSeen = true after a partway disk-full failure, want false")
	}
}
