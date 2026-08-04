package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// allStatuses enumerates every F-41 lifecycle status, in the same order
// they are declared in queue.Status, so status counts print in a stable,
// predictable order every time.
var allStatuses = []queue.Status{
	queue.StatusStaged,
	queue.StatusApproved,
	queue.StatusUploaded,
	queue.StatusRejected,
	queue.StatusAlreadyInPortal,
	queue.StatusFailed,
	queue.StatusSuppressedPeppol,
	queue.StatusDuplicateLinked,
}

// stuckThreshold is F-64's "stuck > 48h" window.
const stuckThreshold = 48 * time.Hour

// stuckEligible are the statuses an item can be "stuck" in — still sitting
// in the local queue awaiting a human or daemon action, rather than
// already at a terminal outcome. staged means "awaiting review or
// approval"; approved means "approved but not yet uploaded" (which can
// happen if the uploader is backing off, or the daemon is not running).
// Terminal statuses (uploaded, rejected, already_in_portal, failed,
// suppressed_peppol, duplicate_linked) are deliberately excluded: they are
// resolved, not stuck, even if old.
var stuckEligible = map[queue.Status]bool{
	queue.StatusStaged:   true,
	queue.StatusApproved: true,
}

// StatusReport is everything `postbode status` prints (F-64): last poll
// time, queue counts by status, the most recently uploaded item's uuid and
// verification state, Gmail token age and re-auth state (F-17 — reported
// as observed state, not a computed expiry; see queue.SyncState's doc
// comment and CLAUDE.md), and every item stuck in staged/approved for more
// than 48h.
type StatusReport struct {
	Now   time.Time
	Sync  queue.SyncState
	Items map[queue.Status][]*queue.Item

	LastUploaded *queue.Item // nil if nothing has ever uploaded
	Stuck        []*queue.Item
}

// Count returns how many items are currently in status st.
func (r StatusReport) Count(st queue.Status) int {
	return len(r.Items[st])
}

// BuildStatusReport reads sync_state and every item, grouped by status,
// and derives the last-uploaded item and the stuck set. now is injected
// (rather than calling time.Now() internally) so tests can control the
// "stuck > 48h" boundary exactly.
func BuildStatusReport(ctx context.Context, db *queue.DB, now time.Time) (StatusReport, error) {
	sync, err := db.GetSyncState(ctx)
	if err != nil {
		return StatusReport{}, fmt.Errorf("cli: BuildStatusReport: %w", err)
	}

	report := StatusReport{
		Now:   now.UTC(),
		Sync:  sync,
		Items: make(map[queue.Status][]*queue.Item, len(allStatuses)),
	}

	var uploaded []*queue.Item
	for _, st := range allStatuses {
		items, err := db.ListByStatus(ctx, st)
		if err != nil {
			return StatusReport{}, fmt.Errorf("cli: BuildStatusReport: %w", err)
		}
		report.Items[st] = items

		if st == queue.StatusUploaded {
			uploaded = items
		}
		if stuckEligible[st] {
			for _, it := range items {
				if report.Now.Sub(it.StagedAt) > stuckThreshold {
					report.Stuck = append(report.Stuck, it)
				}
			}
		}
	}

	sort.Slice(uploaded, func(i, j int) bool {
		ai, aj := uploaded[i].UploadedAt, uploaded[j].UploadedAt
		if ai == nil {
			return false
		}
		if aj == nil {
			return true
		}
		return ai.After(*aj)
	})
	if len(uploaded) > 0 && uploaded[0].UploadedAt != nil {
		report.LastUploaded = uploaded[0]
	}

	sort.Slice(report.Stuck, func(i, j int) bool {
		return report.Stuck[i].StagedAt.Before(report.Stuck[j].StagedAt)
	})

	return report, nil
}

// FormatStatus renders r exactly as `postbode status` prints it (F-64).
func FormatStatus(w io.Writer, r StatusReport) {
	if r.Sync.LastPollAt != nil {
		_, _ = fmt.Fprintf(w, "last poll:        %s (%s ago)\n", formatTime(*r.Sync.LastPollAt), formatAge(r.Now.Sub(*r.Sync.LastPollAt)))
	} else {
		_, _ = fmt.Fprintln(w, "last poll:        never")
	}

	if r.Sync.TokenIssuedAt != nil {
		_, _ = fmt.Fprintf(w, "gmail token:      issued %s (%s ago)\n", formatTime(*r.Sync.TokenIssuedAt), formatAge(r.Now.Sub(*r.Sync.TokenIssuedAt)))
	} else {
		_, _ = fmt.Fprintln(w, "gmail token:      never issued")
	}
	if r.Sync.LastAuthError != "" {
		_, _ = fmt.Fprintf(w, "re-auth needed:   yes (%s)\n", r.Sync.LastAuthError)
	} else {
		_, _ = fmt.Fprintln(w, "re-auth needed:   no")
	}

	_, _ = fmt.Fprintln(w, "queue:")
	for _, st := range allStatuses {
		_, _ = fmt.Fprintf(w, "  %-19s %d\n", string(st), r.Count(st))
	}

	if r.LastUploaded != nil {
		it := r.LastUploaded
		verified := "unverified"
		if it.VerifiedAt != nil {
			verified = formatTime(*it.VerifiedAt)
		}
		_, _ = fmt.Fprintf(w, "last upload:      uuid=%s uploaded %s verified=%s (item #%d)\n",
			it.UUID, formatTime(*it.UploadedAt), verified, it.ID)
	} else {
		_, _ = fmt.Fprintln(w, "last upload:      none yet")
	}

	_, _ = fmt.Fprintf(w, "stuck > 48h:      %d item(s)\n", len(r.Stuck))
	for _, it := range r.Stuck {
		name := it.ProposedFilename
		if name == "" {
			name = it.OrigFilename
		}
		_, _ = fmt.Fprintf(w, "  [%d] %s since %s (%s ago) — %s\n",
			it.ID, it.Status, formatTime(it.StagedAt), formatAge(r.Now.Sub(it.StagedAt)), name)
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// formatAge renders a duration the way a human status line wants it:
// whole hours once past 90 minutes, otherwise whole minutes.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= 90*time.Minute {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
