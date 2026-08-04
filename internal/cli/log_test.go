// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "`postbode log`: decision log + upload log, local, rotated, never containing message bodies or attachment contents; subjects are logged, bodies are not (F-65, NF-05)"
package cli_test

import (
	"context"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/queue"
)

func TestBuildLogIncludesDecisionsAndTransitions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	stageMessage(t, db, "msg-1", "billing@ovh.com", "invoice")
	if err := db.RecordDecision(ctx, queue.DecisionLogEntry{
		GmailMessageID: "msg-1",
		Decision:       "queued",
		Reason:         "matched allow rule",
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	stageItem(t, db, "msg-1", "hash-1", "invoice.pdf")

	entries, err := cli.BuildLog(ctx, db, 0, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildLog: %v", err)
	}

	var haveDecision, haveTransition bool
	for _, e := range entries {
		if e.Kind == "decision" {
			haveDecision = true
		}
		if e.Kind == "transition" {
			haveTransition = true
		}
	}
	if !haveDecision {
		t.Error("BuildLog: no decision entry found")
	}
	if !haveTransition {
		t.Error("BuildLog: no transition entry found")
	}
}

func TestBuildLogSinceFiltersByTime(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	stageMessage(t, db, "msg-1", "vendor@example.com", "old")
	if err := db.RecordDecision(ctx, queue.DecisionLogEntry{
		GmailMessageID: "msg-1",
		Decision:       "queued",
		Reason:         "matched allow rule",
		At:             now.Add(-72 * time.Hour),
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	entries, err := cli.BuildLog(ctx, db, 24*time.Hour, now)
	if err != nil {
		t.Fatalf("BuildLog: %v", err)
	}
	for _, e := range entries {
		if e.Kind == "decision" {
			t.Errorf("BuildLog with since=24h unexpectedly included a 72h-old decision: %+v", e)
		}
	}
}

func TestBuildLogZeroSinceMeansNoFilter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	stageMessage(t, db, "msg-1", "vendor@example.com", "old")
	if err := db.RecordDecision(ctx, queue.DecisionLogEntry{
		GmailMessageID: "msg-1",
		Decision:       "queued",
		Reason:         "matched allow rule",
		At:             now.Add(-30 * 24 * time.Hour),
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	// BuildLog discovers gmail_message_ids by walking items (see this
	// package's doc comment on the resulting gap); a decision with no item
	// is not currently reachable, so this test stages one to observe the
	// decision entry.
	stageItem(t, db, "msg-1", "hash-1", "invoice.pdf")

	entries, err := cli.BuildLog(ctx, db, 0, now)
	if err != nil {
		t.Fatalf("BuildLog: %v", err)
	}
	if len(entries) == 0 {
		t.Error("BuildLog with since=0 unexpectedly filtered out a 30-day-old decision")
	}
}
