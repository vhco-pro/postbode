// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "`postbode status --find <term>` searching by vendor, filename, subject, invoice number and amount, printing exactly one of: `uploaded (uuid, verified-at)` / `staged` / `rejected` / `already-in-portal (marked <date>)` / `unknown` (F-39, G-5, AC-16)"
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// AC-16: after a successful fake upload, `postbode status --find <vendor>`
// prints `uploaded (uuid=<uuid>, verified <ts>)`.
func TestStatusFindUploadedVerdict(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-acerta", "facturatie@acerta.be", "Factuur 2024-118")
	verifiedAt := time.Date(2026, 8, 1, 10, 3, 0, 0, time.UTC)
	stageApproveUpload(t, db, "msg-acerta", "hash-acerta", "invoice-2024-118.pdf", "uuid-acerta-1", verifiedAt)

	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "--find", "acerta"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status --find acerta]) = %d, stderr = %q", code, stderr.String())
	}

	want := "uploaded (uuid=uuid-acerta-1, verified 2026-08-01T10:03:00Z)"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("status --find output missing %q; got:\n%s", want, stdout.String())
	}
}

func TestStatusFindStagedVerdict(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-ovh", "billing@ovh.com", "Uw factuur juli")
	stageItem(t, db, "msg-ovh", "hash-ovh", "invoice-ovh.pdf")

	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "--find", "ovh"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status --find ovh]) = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "-> staged") {
		t.Errorf("status --find output missing staged verdict; got:\n%s", stdout.String())
	}
}

func TestStatusFindRejectedVerdict(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-rej", "vendor@example.com", "spam invoice")
	id := stageItem(t, db, "msg-rej", "hash-rej", "spammy-invoice.pdf")
	if err := db.Reject(context.Background(), id, queue.ActorHuman); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "--find", "spammy"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status --find spammy]) = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "-> rejected") {
		t.Errorf("status --find output missing rejected verdict; got:\n%s", stdout.String())
	}
}

func TestStatusFindAlreadyInPortalVerdict(t *testing.T) {
	home := withTempHome(t)
	db := openQueueAt(t, home)

	stageMessage(t, db, "msg-portal", "vendor@example.com", "handled elsewhere")
	id := stageItem(t, db, "msg-portal", "hash-portal", "peppol-invoice.pdf")
	if err := db.MarkAlreadyInPortalWithTeaching(context.Background(), id, queue.ActorHuman, queue.VendorTeaching{
		VendorDomain: "example.com",
		Reason:       "peppol",
	}); err != nil {
		t.Fatalf("MarkAlreadyInPortalWithTeaching: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "--find", "peppol"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status --find peppol]) = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "-> already-in-portal (marked ") {
		t.Errorf("status --find output missing already-in-portal verdict; got:\n%s", stdout.String())
	}
}

func TestStatusFindUnknownVerdictWhenNoMatch(t *testing.T) {
	home := withTempHome(t)
	openQueueAt(t, home)

	var stdout, stderr bytes.Buffer
	code := run([]string{"status", "--find", "nonexistent-vendor"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([status --find nonexistent-vendor]) = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "unknown") {
		t.Errorf("status --find output missing unknown verdict; got:\n%s", stdout.String())
	}
}
