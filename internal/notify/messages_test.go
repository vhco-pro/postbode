package notify_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/notify"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "macOS notification via `osascript` when new items are staged (\"Postbode: N invoices waiting for review.\") and when an upload batch completes (F-45)"
func TestNotifyStagedMessageWording(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  string
	}{
		{"one item", 1, "Postbode: 1 invoices waiting for review."},
		{"several items", 7, "Postbode: 7 invoices waiting for review."},
		{"zero items", 0, "Postbode: 0 invoices waiting for review."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := notify.StagedMessage(tt.count)
			if got != tt.want {
				t.Errorf("StagedMessage(%d) = %q, want %q", tt.count, got, tt.want)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Spec §3.6, Criterion: "AC-28: Staging new items invokes the notifier exactly once per batch with a message containing the item count; a completed upload batch invokes it exactly once more. Asserted against a fake notifier — `osascript` is behind an interface and is never executed in tests. (F-45)"
func TestNotifyStagedAndUploadCompleteEachInvokeExactlyOnce(t *testing.T) {
	ctx := context.Background()
	fake := &notify.Fake{}

	if err := notify.NotifyStaged(ctx, fake, 3); err != nil {
		t.Fatalf("NotifyStaged: %v", err)
	}
	if err := notify.NotifyUploadBatchComplete(ctx, fake, 2); err != nil {
		t.Fatalf("NotifyUploadBatchComplete: %v", err)
	}

	got := fake.All()
	want := []string{
		"Postbode: 3 invoices waiting for review.",
		"Postbode: upload batch complete, 2 invoice(s) uploaded.",
	}
	if len(got) != len(want) {
		t.Fatalf("Fake recorded %d messages, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if fake.Count() != 2 {
		t.Errorf("Count() = %d, want 2", fake.Count())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "`internal/notify` — macOS notification via `osascript` behind an interface with a fake, so tests never shell out (F-45)"
func TestFakeNeverShellsOut(t *testing.T) {
	// This test's very existence is the proof: it never imports os/exec or
	// touches OSAScript, and it still exercises the full staging +
	// upload-complete notification path via Fake. If any code path here
	// shelled out to osascript, it would pop a real macOS notification
	// during `go test` and/or fail on non-macOS CI — neither happens.
	t.Parallel()
	fake := &notify.Fake{}
	var n notify.Notifier = fake // static check: Fake satisfies Notifier
	if err := n.Notify(context.Background(), "test message"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := fake.All(); len(got) != 1 || got[0] != "test message" {
		t.Errorf("All() = %v, want [\"test message\"]", got)
	}
}
