package extract_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/queue"
)

// The window this whole flag exists for: ExtractMessage records the message
// as seen BEFORE staging its documents, so a failure in between leaves a
// message that is recorded but has no queue rows. recordThenFail reproduces
// that state exactly — by letting a real extraction record the message and
// then failing every candidate at the Gate, which is the same observable
// outcome as a staging failure: recorded, nothing queued.
func recordThenFail(t *testing.T, ex *extract.Extractor, db *queue.DB, id string, raw []byte) {
	t.Helper()
	ctx := context.Background()

	ex.Gate = func(extract.Candidate) (bool, string) { return false, "simulated staging failure" }
	defer func() { ex.Gate = nil }()

	if _, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: id,
		From:           "billing@ovh.com",
		Raw:            raw,
	}); err != nil {
		t.Fatalf("setup extraction: %v", err)
	}

	// Precondition for every test below: the message IS recorded, and it has
	// NO queue rows. If this ever stops being reachable the tests are
	// asserting nothing.
	seen, err := db.MessageSeen(ctx, id)
	if err != nil {
		t.Fatalf("MessageSeen: %v", err)
	}
	if !seen {
		t.Fatalf("setup did not record message %s; the F-78 window is not being reproduced", id)
	}
	items, err := db.ItemsByMessageID(ctx, id)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("setup left %d queue row(s) for %s, want 0", len(items), id)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-45 — the silent-miss guard: A message that reaches RecordMessageIfNew and then fails during staging is parked; on retry it is re-extracted rather than L1-skipped, and its documents reach the queue."
func TestForceReextractRecoversAMessageStuckBehindL1(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	raw := loadFixture(t, "nested-three-pdfs.eml")
	const id = "msg-recorded-then-failed"

	recordThenFail(t, ex, db, id, raw)

	// Without the bypass this is what a retry gets: a silent, successful,
	// empty skip. No error, no log line, and the invoice gone forever.
	plain, err := ex.ExtractMessage(ctx, extract.Message{GmailMessageID: id, From: "billing@ovh.com", Raw: raw})
	if err != nil {
		t.Fatalf("plain retry: %v", err)
	}
	if !plain.Skipped {
		t.Fatal("a plain retry was NOT L1-skipped; the hazard this flag guards has changed shape and ADR-005 needs revisiting")
	}
	if items, _ := db.ItemsByMessageID(ctx, id); len(items) != 0 {
		t.Fatalf("plain retry staged %d item(s); expected the silent skip", len(items))
	}

	// With the bypass, the documents actually reach the queue.
	forced, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: id,
		From:           "billing@ovh.com",
		Raw:            raw,
		ForceReextract: true,
	})
	if err != nil {
		t.Fatalf("forced retry: %v", err)
	}
	if forced.Skipped {
		t.Fatalf("forced retry was still skipped (%s); the L1 bypass did not take", forced.SkipReason)
	}
	if len(forced.Items) != 3 {
		t.Fatalf("forced retry produced %d item(s), want 3", len(forced.Items))
	}
	items, err := db.ItemsByMessageID(ctx, id)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("queue rows after the forced retry = %d, want 3 — this is the invoice that would otherwise be lost", len(items))
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-45: ... The retry produces no second uploadable item for a byte-identical document — the duplicate links as duplicate_linked via L2 exactly as AC-11 requires"
func TestForceReextractStillHonoursL2(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	raw := loadFixture(t, "nested-three-pdfs.eml")
	const id = "msg-forced-twice"

	recordThenFail(t, ex, db, id, raw)

	// First forced retry: the documents land.
	if _, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: id, From: "billing@ovh.com", Raw: raw, ForceReextract: true,
	}); err != nil {
		t.Fatalf("first forced retry: %v", err)
	}

	// A second forced retry of the same bytes must NOT produce a second
	// uploadable copy: the bypass is L1-only, and L2 is what stops it.
	if _, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: id, From: "billing@ovh.com", Raw: raw, ForceReextract: true,
	}); err != nil {
		t.Fatalf("second forced retry: %v", err)
	}

	items, err := db.ItemsByMessageID(ctx, id)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}

	uploadable := 0
	linked := 0
	for _, it := range items {
		switch it.Status {
		case queue.StatusDuplicateLinked:
			linked++
			if it.DedupLayer != queue.DedupLayerL2 {
				t.Errorf("item %d linked via %q, want L2", it.ID, it.DedupLayer)
			}
			if it.LinkedItemID == nil {
				t.Errorf("item %d is duplicate_linked with no linked_item_id", it.ID)
			}
		case queue.StatusStaged:
			uploadable++
		}
	}
	if uploadable != 3 {
		t.Errorf("uploadable (staged) items = %d, want 3 — a forced retry must never multiply the queue", uploadable)
	}
	if linked != 3 {
		t.Errorf("duplicate_linked items = %d, want 3 (the second pass, linked rather than dropped)", linked)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-45: ... and a document previously rejected stays rejected via F-44."
func TestForceReextractStillHonoursRejectionMemory(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	raw := loadFixture(t, "nested-three-pdfs.eml")
	const id = "msg-rejected-then-forced"

	// Stage normally, then reject everything: that is what arms F-44's
	// rejection memory for this (message, sha256) pair.
	res, err := ex.ExtractMessage(ctx, extract.Message{GmailMessageID: id, From: "billing@ovh.com", Raw: raw})
	if err != nil {
		t.Fatalf("initial extraction: %v", err)
	}
	if len(res.Items) == 0 {
		t.Fatal("initial extraction staged nothing")
	}
	for _, it := range res.Items {
		if err := db.Reject(ctx, it.Stage.ItemID, queue.ActorHuman); err != nil {
			t.Fatalf("Reject(%d): %v", it.Stage.ItemID, err)
		}
	}

	// A forced re-extraction must not resurrect them: the human already
	// said no, and the bypass is L1-only.
	if _, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: id, From: "billing@ovh.com", Raw: raw, ForceReextract: true,
	}); err != nil {
		t.Fatalf("forced retry after rejection: %v", err)
	}

	items, err := db.ItemsByMessageID(ctx, id)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	for _, it := range items {
		if it.Status == queue.StatusStaged {
			t.Errorf("item %d is staged again after a human rejected it; F-44 rejection memory was bypassed", it.ID)
		}
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "5g. Add a test asserting the bypass does not leak: an ordinary listed message that is already recorded still L1-skips with the skip (L1) line (F-30 replay protection intact) — including on an F-13 fallback resync, which re-lists old ids."
func TestBypassDoesNotLeakToOrdinaryMessages(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	ex := extract.New(spoolDir(t), db)
	raw := loadFixture(t, "nested-three-pdfs.eml")
	const id = "msg-ordinary"

	if _, err := ex.ExtractMessage(ctx, extract.Message{GmailMessageID: id, From: "billing@ovh.com", Raw: raw}); err != nil {
		t.Fatalf("first extraction: %v", err)
	}

	// The F-13 fallback re-lists old ids on every resync. Without an intact
	// L1 skip that would re-extract the whole mailbox, every time.
	for i := 0; i < 3; i++ {
		again, err := ex.ExtractMessage(ctx, extract.Message{GmailMessageID: id, From: "billing@ovh.com", Raw: raw})
		if err != nil {
			t.Fatalf("resync %d: %v", i, err)
		}
		if !again.Skipped {
			t.Fatalf("resync %d was not L1-skipped; F-30 replay protection is broken", i)
		}
	}

	items, err := db.ItemsByMessageID(ctx, id)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("queue rows after three resyncs = %d, want the original 3", len(items))
	}
}

// The bypass must not weaken the spool-before-record fail-safe: a forced
// re-extraction that cannot spool still records nothing and stages nothing.
func TestForceReextractKeepsTheSpoolFailSafe(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	raw := loadFixture(t, "nested-three-pdfs.eml")
	const id = "msg-diskfull-forced"

	diskFull := errors.New("simulated disk full: no space left on device")
	ex := extract.NewWithSpooler(extract.NewFailingSpooler(spoolDir(t), diskFull), db)

	_, err := ex.ExtractMessage(ctx, extract.Message{
		GmailMessageID: id, From: "billing@ovh.com", Raw: raw, ForceReextract: true,
	})
	if err == nil {
		t.Fatal("a forced extraction with a failing spooler returned nil error")
	}
	if !errors.Is(err, diskFull) {
		t.Errorf("error = %v, want it to wrap the spool failure", err)
	}
	if items, _ := db.ItemsByMessageID(ctx, id); len(items) != 0 {
		t.Errorf("staged %d item(s) despite a spool failure, want 0", len(items))
	}
}
