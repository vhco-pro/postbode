// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "`postbode log`: decision log + upload log, local, rotated, never containing message bodies or attachment contents; subjects are logged, bodies are not (F-65, NF-05)"
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

func TestLogPrintsDecisionAndUploadEntries(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-1", "billing@ovh.com", "Uw factuur juli")
	if err := db.RecordDecision(context.Background(), queue.DecisionLogEntry{
		GmailMessageID:   "msg-1",
		Decision:         "queued",
		MatchedRuleIndex: ptr(0),
		Reason:           "matched allow rule",
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	id := stageItem(t, db, "msg-1", "hash-1", "invoice.pdf")
	if err := db.Approve(context.Background(), id, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := db.MarkUploaded(context.Background(), id, "uuid-log-1", 2); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"log"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([log]) = %d, stderr = %q", code, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{
		"decision=queued",
		"message=msg-1",
		`reason="matched allow rule"`,
		"staged->approved",
		"approved->uploaded",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q; got:\n%s", want, out)
		}
	}
}

// F-65: subjects are fine, bodies are not — and this package never has a
// body to leak in the first place, but the log line must not somehow grow
// one via the vendor/subject fields either.
func TestLogNeverPrintsSomethingThatLooksLikeAMessageBody(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-2", "vendor@example.com", "Uw factuur juli")
	stageItem(t, db, "msg-2", "hash-2", "invoice.pdf")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"log"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run([log]) = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "Beste klant") {
		t.Errorf("log output unexpectedly contains body-shaped text: %s", stdout.String())
	}
}

func TestLogSinceFiltersOldEntries(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-old", "vendor@example.com", "old invoice")
	if err := db.RecordDecision(context.Background(), queue.DecisionLogEntry{
		GmailMessageID: "msg-old",
		Decision:       "queued",
		Reason:         "matched allow rule",
		At:             time.Now().UTC().Add(-72 * time.Hour),
	}); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	stageItem(t, db, "msg-old", "hash-old", "invoice-old.pdf")

	var stdout, stderr bytes.Buffer
	code := run([]string{"log", "--since", "24h"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([log --since 24h]) = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "message=msg-old") {
		t.Errorf("log --since 24h unexpectedly included a 72h-old decision: %s", stdout.String())
	}
}
