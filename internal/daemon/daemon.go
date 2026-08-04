// Package daemon wires internal/gmailwatch, internal/uploader,
// internal/webui and internal/notify into Postbode's long-running
// process (`postbode daemon` / `postboded`, F-60, F-61). It exists so the
// poll-loop/upload-loop/prune-tick orchestration is testable against
// fakes with a cancellable context and tiny intervals — cmd/postbode's
// main.go does only flag parsing, real credential/service wiring and
// process signal handling; every decision about WHEN to poll, WHEN to
// upload and HOW a recoverable failure degrades lives here (Phase 13).
//
// # NF-06 — nothing here ever propagates a recoverable failure as fatal
//
// Every method that talks to Gmail, ClearFacts or the filesystem already
// classifies its own failures as recoverable (internal/gmailwatch's F-16
// re-auth handling, internal/uploader's F-51 retry/give-up classification,
// internal/clearfacts's retryable/notify-worthy split). Daemon's job is
// narrower: log the outcome, keep the loop alive, and — for F-15's label
// resolution — degrade the uploader specifically rather than the whole
// process. Run's only non-nil error return is unreachable in production;
// it exists so a test can assert Run returns cleanly on context
// cancellation.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/uploader"
)

// defaultPollInterval matches config.Default().Gmail.PollInterval.
const defaultPollInterval = 5 * time.Minute

// defaultPruneInterval is how often PruneOnce runs on its own tick.
const defaultPruneInterval = 24 * time.Hour

// defaultStaleClaimAge is the startup ReleaseStaleClaims age (uploader.go's
// own doc: must be "long enough that a live uploader instance's own
// in-flight claim could never look stale" — a single upload attempt is
// single-digit seconds to low minutes, so 1h is generous headroom).
const defaultStaleClaimAge = time.Hour

// defaultRetentionDays matches config.Default().RetentionDays.
const defaultRetentionDays = 30

// Daemon owns the poll/upload/prune orchestration loop.
type Daemon struct {
	// DB is the queue store. Required.
	DB *queue.DB
	// Watcher runs the Gmail poll cycle and resolves the submitted label.
	// Required.
	Watcher *gmailwatch.Watcher
	// Uploader drains approved items and prunes the spool. Required.
	Uploader *uploader.Uploader
	// Notifier sends the F-15 "uploads paused" notification. Nil is a
	// valid, silent no-op.
	Notifier notify.Notifier

	// PollInterval is the Gmail poll cadence (gmail.poll_interval,
	// default 5m per config.Default()). <= 0 uses the default.
	PollInterval time.Duration
	// PruneInterval is how often PruneOnce runs on its own tick. <= 0
	// uses the default (24h) — not itself an F-id-numbered knob, but the
	// prune work (F-24) needs some cadence and daily is more than
	// sufficient for a retention window measured in days.
	PruneInterval time.Duration
	// RetentionDays is passed to Uploader.PruneSpool (F-24, default 30
	// per config.Default()). <= 0 uses the default.
	RetentionDays int
	// StaleClaimAge is passed to Uploader.ReleaseStaleClaims once at
	// startup (F-52). <= 0 uses the default (1h) — see
	// defaultStaleClaimAge's doc for why this must never be 0.
	StaleClaimAge time.Duration

	// Logf, when set, receives one line per notable event. Nil is a
	// valid, silent default — mirrors gmailwatch.Watcher.Logf and
	// uploader.Uploader.Logf's convention.
	Logf func(format string, args ...any)

	mu            sync.Mutex
	labelResolved bool
}

func (d *Daemon) logf(format string, args ...any) {
	if d.Logf != nil {
		d.Logf(format, args...)
	}
}

func (d *Daemon) pollInterval() time.Duration {
	if d.PollInterval <= 0 {
		return defaultPollInterval
	}
	return d.PollInterval
}

func (d *Daemon) pruneInterval() time.Duration {
	if d.PruneInterval <= 0 {
		return defaultPruneInterval
	}
	return d.PruneInterval
}

func (d *Daemon) staleClaimAge() time.Duration {
	if d.StaleClaimAge <= 0 {
		return defaultStaleClaimAge
	}
	return d.StaleClaimAge
}

// retentionDays defaults only an unset (zero) value. A negative value is
// left as-is — extract.PruneUploadedItem's own age-vs-threshold check
// then treats it as "prune immediately," which a test uses to exercise
// PruneOnce without waiting real days.
func (d *Daemon) retentionDays() int {
	if d.RetentionDays == 0 {
		return defaultRetentionDays
	}
	return d.RetentionDays
}

// LabelResolved reports whether the F-15 submitted-label lookup has
// succeeded yet — false means the uploader is currently paused.
func (d *Daemon) LabelResolved() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.labelResolved
}

// ensureLabelResolved implements F-15's scope: a genuinely absent label
// (gmailwatch.ErrLabelNotFound — an authenticated lookup that came back
// without the name) disables the uploader, but is retried on every call
// rather than only once, so a human fixing the label recovers uploads
// without a daemon restart. Any other resolution failure (auth, network,
// pending re-auth) is routine per F-16/OQ-P7 and is likewise silently
// retried — never treated as "absent," and never notified as a distinct
// condition (Poll's own F-16 handling already notifies re-auth).
func (d *Daemon) ensureLabelResolved(ctx context.Context) bool {
	d.mu.Lock()
	resolved := d.labelResolved
	d.mu.Unlock()
	if resolved {
		return true
	}

	err := d.Watcher.ResolveAndPersistSubmittedLabel(ctx)
	if err == nil {
		d.mu.Lock()
		d.labelResolved = true
		d.mu.Unlock()
		return true
	}
	if errors.Is(err, gmailwatch.ErrLabelNotFound) {
		d.logf("daemon: label %q not found, refusing to start the uploader (F-15): %v", gmailwatch.SubmittedLabelName, err)
		if d.Notifier != nil {
			_ = d.Notifier.Notify(ctx, fmt.Sprintf("Postbode: label %q not found. Uploads are paused until it exists in Gmail.", gmailwatch.SubmittedLabelName))
		}
		return false
	}
	d.logf("daemon: submitted label not yet resolved, will retry next cycle: %v", err)
	return false
}

// RunIteration runs exactly one poll pass and, only if the F-15 label is
// resolved, one upload batch. Every failure is logged and swallowed
// (NF-06) — this method never returns an error.
func (d *Daemon) RunIteration(ctx context.Context) {
	if _, err := d.Watcher.Poll(ctx); err != nil {
		d.logf("daemon: poll: %v", err)
	}
	if !d.ensureLabelResolved(ctx) {
		return
	}
	if _, err := d.Uploader.RunBatch(ctx); err != nil {
		d.logf("daemon: upload: %v", err)
	}
}

// PruneOnce runs one F-24 spool-pruning pass.
func (d *Daemon) PruneOnce(ctx context.Context) {
	n, errs := d.Uploader.PruneSpool(ctx, d.retentionDays())
	for _, e := range errs {
		d.logf("daemon: prune: %v", e)
	}
	if n > 0 {
		d.logf("daemon: pruned %d spool file(s)", n)
	}
}

// Run releases stale claims once (F-52 — see defaultStaleClaimAge), runs
// one iteration immediately so the daemon does not sit idle for a full
// PollInterval before its first poll, then drives RunIteration on
// PollInterval and PruneOnce on PruneInterval until ctx is cancelled.
//
// Shutdown is graceful by construction, not by any special-cased code
// path here: SIGINT/SIGTERM cancels ctx (the caller's job, see
// cmd/postbode), which this select observes only between iterations —
// an in-flight Poll/RunBatch call is left to finish (or to fail its own
// ctx-aware network call, which its own recoverable-failure handling
// already covers) rather than being torn down mid-write. A claimed item
// that genuinely dies with the process (kill -9, not a graceful ctx
// cancel) is exactly what ReleaseStaleClaims at the next startup exists
// to recover — see uploader.Uploader.ReleaseStaleClaims's own doc.
func (d *Daemon) Run(ctx context.Context) error {
	if _, err := d.Uploader.ReleaseStaleClaims(ctx, d.staleClaimAge()); err != nil {
		d.logf("daemon: release stale claims at startup: %v", err)
	}

	d.RunIteration(ctx)

	pollTicker := time.NewTicker(d.pollInterval())
	defer pollTicker.Stop()
	pruneTicker := time.NewTicker(d.pruneInterval())
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pollTicker.C:
			d.RunIteration(ctx)
		case <-pruneTicker.C:
			d.PruneOnce(ctx)
		}
	}
}
