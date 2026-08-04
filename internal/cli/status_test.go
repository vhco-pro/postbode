// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "`postbode status`: last poll time, queue counts by status, last upload uuid, Gmail token age/expiry, `re-auth needed` flag, and items stuck > 48h (F-64, F-17, AC-20 print half)"
package cli_test

import (
	"context"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/queue"
)

func TestBuildStatusReportCountsByStatus(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, "msg-1", "vendor@example.com", "invoice 1")
	stageItem(t, db, "msg-1", "hash-1", "a.pdf")

	stageMessage(t, db, "msg-2", "vendor@example.com", "invoice 2")
	id2 := stageItem(t, db, "msg-2", "hash-2", "b.pdf")
	if err := db.Reject(ctx, id2, queue.ActorHuman); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	report, err := cli.BuildStatusReport(ctx, db, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	if got := report.Count(queue.StatusStaged); got != 1 {
		t.Errorf("Count(staged) = %d, want 1", got)
	}
	if got := report.Count(queue.StatusRejected); got != 1 {
		t.Errorf("Count(rejected) = %d, want 1", got)
	}
	if got := report.Count(queue.StatusUploaded); got != 0 {
		t.Errorf("Count(uploaded) = %d, want 0", got)
	}
}

func TestBuildStatusReportLastUploadedIsMostRecent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, "msg-a", "vendor@example.com", "invoice a")
	idA := stageItem(t, db, "msg-a", "hash-a", "a.pdf")
	if err := db.Approve(ctx, idA, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := db.MarkUploaded(ctx, idA, "uuid-a", 1); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}

	stageMessage(t, db, "msg-b", "vendor@example.com", "invoice b")
	idB := stageItem(t, db, "msg-b", "hash-b", "b.pdf")
	if err := db.Approve(ctx, idB, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := db.MarkUploaded(ctx, idB, "uuid-b", 1); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}

	report, err := cli.BuildStatusReport(ctx, db, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	if report.LastUploaded == nil {
		t.Fatal("LastUploaded is nil, want the most recently uploaded item")
	}
	if report.LastUploaded.UUID != "uuid-b" {
		t.Errorf("LastUploaded.UUID = %q, want %q (uploaded after uuid-a)", report.LastUploaded.UUID, "uuid-b")
	}
}

func TestBuildStatusReportStuckOnlyCountsStagedAndApprovedPast48h(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	stageMessage(t, db, "msg-fresh", "vendor@example.com", "fresh")
	stageItem(t, db, "msg-fresh", "hash-fresh", "fresh.pdf")

	stageMessage(t, db, "msg-old", "vendor@example.com", "old")
	oldID := stageItem(t, db, "msg-old", "hash-old", "old.pdf")

	report, err := cli.BuildStatusReport(ctx, db, now.Add(49*time.Hour))
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	// Both items were staged "now" relative to db writes, so from the
	// perspective of now+49h both look stuck: this test only asserts the
	// count and that oldID's item is among them, not that "fresh" is
	// excluded (that boundary is exercised at the CLI level in
	// cmd/postbode/status_test.go against a backdated staged_at).
	found := false
	for _, it := range report.Stuck {
		if it.ID == oldID {
			found = true
		}
	}
	if !found {
		t.Errorf("Stuck does not include item %d", oldID)
	}
	if len(report.Stuck) != 2 {
		t.Errorf("len(Stuck) = %d, want 2 (both staged items, none uploaded/rejected)", len(report.Stuck))
	}
}

func TestBuildStatusReportNothingStuckWithinWindow(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, "msg-1", "vendor@example.com", "invoice")
	stageItem(t, db, "msg-1", "hash-1", "a.pdf")

	report, err := cli.BuildStatusReport(ctx, db, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildStatusReport: %v", err)
	}
	if len(report.Stuck) != 0 {
		t.Errorf("len(Stuck) = %d, want 0 for an item staged moments ago", len(report.Stuck))
	}
}
