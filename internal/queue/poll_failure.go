package queue

import (
	"context"
	"fmt"
	"time"
)

// RecordPollFailure records one poll cycle that ended WITHOUT persisting
// sync_state, and reports whether this is the moment to escalate (F-81,
// F-82).
//
// "Did not persist sync_state" is deliberately the trigger rather than
// "returned an error": that single predicate covers history.list failing,
// the F-13 fallback query failing, SaveSyncState itself failing, and F-71's
// under-budget per-message abort — the four ways the 2026-08-22 outage could
// have started. Anything that makes no forward progress counts, however it
// phrased its failure.
//
// escalate is true EXACTLY ONCE per stall episode, gated on the persisted
// poll_stall_notified_at marker so a daemon restarted mid-stall does not
// re-announce a problem the human already knows about. A successful poll
// ends the episode (ClearPollFailure); a later stall is a NEW episode and
// escalates again.
func (db *DB) RecordPollFailure(ctx context.Context, errText string, budget int, now time.Time) (SyncState, bool, error) {
	if budget < 1 {
		budget = 1
	}
	now = now.UTC()

	st, err := db.GetSyncState(ctx)
	if err != nil {
		return SyncState{}, false, err
	}

	st.ConsecutivePollFailures++
	st.LastPollError = TruncateError(errText)
	if st.FirstPollFailureAt == nil {
		first := now
		st.FirstPollFailureAt = &first
	}

	escalate := st.ConsecutivePollFailures >= budget && st.PollStallNotifiedAt == nil
	if escalate {
		marker := now
		st.PollStallNotifiedAt = &marker
	}

	// Note this writes sync_state — which is not the same thing as the poll
	// having "persisted sync_state" in F-81's sense. This call deliberately
	// does not touch history_id or last_poll_at, so it records the failure
	// without recording progress that did not happen.
	if err := db.SaveSyncState(ctx, st); err != nil {
		return SyncState{}, false, fmt.Errorf("queue: record poll failure: %w", err)
	}
	return st, escalate, nil
}

// ClearPollFailure ends the current stall episode: counter to zero, episode
// anchor and notify-once marker cleared. Called by any poll that persists
// sync_state, so the very next stall is treated as new and may notify again.
func (db *DB) ClearPollFailure(st *SyncState) {
	st.ConsecutivePollFailures = 0
	st.FirstPollFailureAt = nil
	st.LastPollError = ""
	st.PollStallNotifiedAt = nil
}
