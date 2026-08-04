// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "`postbode status`: last poll time, queue counts by status, last upload uuid, Gmail token age/expiry, `re-auth needed` flag, and items stuck > 48h (F-64, F-17, AC-20 print half)"
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

func TestStatusPrintsPollCountsAndLastUpload(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	if err := db.SaveSyncState(context.Background(), queue.SyncState{
		LastPollAt:    ptr(time.Now().UTC().Add(-5 * time.Minute)),
		TokenIssuedAt: ptr(time.Now().UTC().Add(-2 * time.Hour)),
	}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	stageMessage(t, db, "msg-staged", "vendor@example.com", "Factuur 1")
	stageItem(t, db, "msg-staged", "hash-staged", "invoice-staged.pdf")

	verifiedAt := time.Now().UTC().Add(-1 * time.Minute)
	stageApproveUpload(t, db, "msg-uploaded", "hash-uploaded", "invoice-uploaded.pdf", "uuid-1234", verifiedAt)

	var stdout, stderr bytes.Buffer
	code := run([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status]) = %d, stderr = %q", code, stderr.String())
	}

	out := stdout.String()
	for _, want := range []string{
		"last poll:",
		"gmail token:",
		"re-auth needed:   no",
		"staged              1",
		"uploaded            1",
		"uuid=uuid-1234",
		"stuck > 48h:      0 item(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}

func TestStatusReportsReauthNeeded(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	if err := db.SaveSyncState(context.Background(), queue.SyncState{
		LastAuthError: "invalid_grant",
	}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status]) = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "re-auth needed:   yes (invalid_grant)") {
		t.Errorf("status output missing re-auth needed line; got:\n%s", stdout.String())
	}
}

// AC-20's print half: `postbode status` reports re-auth needed with token age.
func TestStatusReauthNeededIncludesTokenAge(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	if err := db.SaveSyncState(context.Background(), queue.SyncState{
		LastAuthError: "invalid_grant",
		TokenIssuedAt: ptr(time.Now().UTC().Add(-3 * time.Hour)),
	}); err != nil {
		t.Fatalf("SaveSyncState: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run([status]) = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "gmail token:      issued") || !strings.Contains(out, "3h ago") {
		t.Errorf("status output missing token age; got:\n%s", out)
	}
}

func TestStatusExitsNonZeroWhenItemsStuckOver48h(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-stuck", "vendor@example.com", "Factuur oud")
	id := stageItem(t, db, "msg-stuck", "hash-stuck", "invoice-stuck.pdf")

	// Backdate staged_at to 49h ago directly, since StageItem always stamps
	// "now" and F-64's "stuck" is defined relative to staged_at.
	_ = db.Close() // release the pooled connection before the raw one writes
	backdateStagedAt(t, home, id, time.Now().UTC().Add(-49*time.Hour))

	var stdout, stderr bytes.Buffer
	code := run([]string{"status"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("run([status]) = 0, want non-zero with a 49h-old staged item; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "stuck > 48h:      1 item(s)") {
		t.Errorf("status output missing stuck item; got:\n%s", stdout.String())
	}
}

func TestStatusExitsZeroWhenNothingStuck(t *testing.T) {
	home := withTempHome(t)
	openQueueAt(t, home)

	var stdout, stderr bytes.Buffer
	code := run([]string{"status"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status]) on an empty queue = %d, want 0; stderr = %q", code, stderr.String())
	}
}

func ptr[T any](v T) *T { return &v }
