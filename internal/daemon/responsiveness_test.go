// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "**`postbode daemon` / `postboded`** — the currently-unimplemented dispatch case in `cmd/postbode/main.go`"
package daemon

import (
	"context"
	"testing"
	"time"
)

// Approving in the UI used to do nothing visible for up to a full
// PollInterval, because uploads rode the poll ticker. Five minutes of a
// correct system looking broken is a defect in its own right.
func TestNudgeIsNonBlockingAndCoalesces(t *testing.T) {
	d := &Daemon{}
	done := make(chan struct{})
	go func() {
		// Far more nudges than the buffer depth: none may block.
		for i := 0; i < 1000; i++ {
			d.Nudge()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Nudge blocked; it is called on the HTTP request path and must never block")
	}
	// Exactly one pending signal survives — a drain already queued will pick
	// up everything approved since, so coalescing is correct.
	select {
	case <-d.nudgeChan():
	default:
		t.Fatal("no pending nudge after 1000 calls; the signal was lost")
	}
	select {
	case <-d.nudgeChan():
		t.Fatal("more than one pending nudge; signals should coalesce, not queue up")
	default:
	}
}

// The upload cadence must be independent of, and much faster than, the
// Gmail poll cadence.
func TestUploadIntervalIsIndependentOfPollInterval(t *testing.T) {
	d := &Daemon{PollInterval: 5 * time.Minute}
	if d.uploadInterval() >= d.pollInterval() {
		t.Errorf("uploadInterval %v >= pollInterval %v: approvals would again wait on the poll tick",
			d.uploadInterval(), d.pollInterval())
	}
	if d.uploadInterval() > 30*time.Second {
		t.Errorf("uploadInterval %v is too slow to feel responsive after pressing Approve", d.uploadInterval())
	}
	custom := &Daemon{UploadInterval: 3 * time.Second}
	if custom.uploadInterval() != 3*time.Second {
		t.Errorf("UploadInterval override ignored: got %v", custom.uploadInterval())
	}
}

// Nudge must be safe before Run has ever been called.
func TestNudgeBeforeRunDoesNotPanic(t *testing.T) {
	d := &Daemon{}
	d.Nudge()
	_ = context.Background()
}
