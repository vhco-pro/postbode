package gmailwatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"time"

	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/rules"
)

// Poll runs exactly one poll cycle: incremental history sync (F-12) or,
// on first run or a history gap, the F-13 windowed fallback; extraction and
// rules-gated staging for every candidate message (see the Watcher package
// doc); a single F-45 staging notification if anything queued; and
// sync_state persistence (F-12, F-17). It never panics and never returns a
// fatal error for an OAuth refresh failure — see handleReauth (F-16).
func (w *Watcher) Poll(ctx context.Context) (PollResult, error) {
	st, err := w.DB.GetSyncState(ctx)
	if err != nil {
		return PollResult{}, fmt.Errorf("gmailwatch: poll: read sync_state: %w", err)
	}

	var (
		msgIDs       []string
		newHistoryID string
		usedFallback bool
	)

	switch st.HistoryID {
	case "":
		// First ever poll (F-13).
		msgIDs, err = w.fallbackList(ctx)
		usedFallback = true
	default:
		var startID uint64
		startID, err = strconv.ParseUint(st.HistoryID, 10, 64)
		if err != nil {
			// A corrupt/unparseable stored historyId is exactly the same
			// situation as a history gap from Gmail's point of view: fall
			// back rather than fail.
			err = ErrHistoryGap
		} else {
			msgIDs, newHistoryID, err = w.historySync(ctx, startID)
		}
		if errors.Is(err, ErrHistoryGap) {
			msgIDs, err = w.fallbackList(ctx)
			usedFallback = true
		}
	}

	if err != nil {
		if reason, ok := isReauthError(err); ok {
			return w.handleReauth(ctx, st, reason)
		}
		return w.failPoll(ctx, PollResult{}, fmt.Errorf("gmailwatch: poll: %w", err))
	}

	// The ids that currently carry a failure row, read once per cycle so a
	// healthy message costs no write to "reset" a counter it never had
	// (F-70, NF-18).
	failedIDs, err := w.DB.FailedIDs(ctx)
	if err != nil {
		return w.failPoll(ctx, PollResult{}, fmt.Errorf("gmailwatch: poll: read failure state: %w", err))
	}

	// Retry admission (F-75, F-77). Parking advances history_id past the
	// parked message by design, so it will never be listed again — by id is
	// the ONLY way back in. Both the automatic cooldown and `postbode
	// retry` work through this one mechanism, which is also why `retry` can
	// be a plain SQLite write from a separate process.
	//
	// Admitted ids go FIRST: a message that has been waiting hours for its
	// retry should not queue behind a fresh listing, and if the cycle later
	// aborts under budget, the retry has already had its attempt.
	retried, err := w.DB.DueRetries(ctx, w.now())
	if err != nil {
		return w.failPoll(ctx, PollResult{}, fmt.Errorf("gmailwatch: poll: read due retries: %w", err))
	}
	admitted := map[string]bool{}
	if len(retried) > 0 {
		for _, id := range retried {
			admitted[id] = true
			if w.Logf != nil {
				w.Logf("retry: message %s admitted for another attempt", id)
			}
		}
		// De-duplicate against the listing, so a message that is both due
		// and listed is processed exactly once — and processed as a retry,
		// since that is the stricter path (L1 bypass, effective budget 1).
		deduped := make([]string, 0, len(retried)+len(msgIDs))
		deduped = append(deduped, retried...)
		for _, id := range msgIDs {
			if !admitted[id] {
				deduped = append(deduped, id)
			}
		}
		msgIDs = deduped

		// Spend one automatic attempt per admitted message per cycle
		// (F-75). Done up front, before processing: if the attempt fails,
		// crashes or the daemon dies mid-cycle, the attempt is still spent,
		// so a broken message can never loop faster than the schedule
		// allows.
		for _, id := range retried {
			if err := w.DB.RecordRetryAttempt(ctx, id, w.now(), w.Config.parkRetryCooldown(), w.Config.parkRetryAttempts()); err != nil {
				return w.failPoll(ctx, PollResult{}, fmt.Errorf("gmailwatch: poll: record retry attempt for %s: %w", id, err))
			}
		}
	}

	var (
		staged int
		parked []string
	)
	for _, id := range msgIDs {
		n, perr := w.processMessage(ctx, id, admitted[id])
		if perr != nil {
			if reason, ok := isReauthError(perr); ok {
				return w.handleReauth(ctx, st, reason)
			}
			if isMessageGone(perr) {
				// The message was listed and then deleted (or purged from
				// Trash) before we fetched it. It cannot be missed, because
				// it no longer exists in the mailbox — so skipping it is not
				// a G-1 silent miss, whereas returning here is a permanent
				// outage: the error aborts the loop before sync_state is
				// persisted, so historyId never advances and the very next
				// poll re-lists the same dead id and fails identically,
				// forever. Observed in production: one deleted message
				// wedged the daemon for days while real invoices piled up
				// unseen behind it.
				//
				// It also clears any park: a message that was parked and has
				// since been deleted provably cannot be a silent miss, and
				// leaving it in the parked report would be noise a human can
				// do nothing about.
				if failedIDs[id] {
					if cerr := w.DB.ClearMessageFailure(ctx, id); cerr != nil {
						return w.failPoll(ctx, PollResult{StagedCount: staged, Parked: parked}, fmt.Errorf("gmailwatch: poll: clear failure state for gone message %s: %w", id, cerr))
					}
				}
				if w.Logf != nil {
					w.Logf("skip (gone): message %s no longer exists in Gmail (404)", id)
				}
				continue
			}
			if !consumesBudget(perr) {
				// Context cancellation: the daemon is shutting down, not the
				// message failing. Abort the cycle without charging anyone —
				// see consumesBudget for why charging here would park
				// perfectly healthy mail after a few restarts.
				return PollResult{StagedCount: staged, Parked: parked}, fmt.Errorf("gmailwatch: poll: process message %s: %w", id, perr)
			}

			f, newlyParked, rerr := w.recordFailure(ctx, id, perr)
			if rerr != nil {
				return w.failPoll(ctx, PollResult{StagedCount: staged, Parked: parked}, rerr)
			}
			failedIDs[id] = true

			if f.Parked() {
				// F-72: the whole point. The loop continues, the cycle
				// reaches SaveSyncState, history_id advances, and every
				// message behind this one is processed.
				if newlyParked {
					parked = append(parked, id)
					if w.Notifier != nil {
						_ = w.Notifier.Notify(ctx, notify.ParkedMessage(id, f.FailureCount, f.LastError))
					}
					if w.Logf != nil {
						w.Logf("parked: message %s after %d consecutive failures: %s", id, f.FailureCount, f.LastError)
					}
				} else if w.Logf != nil {
					w.Logf("re-parked: message %s (park #%d, %d failures): %s", id, f.ParkCount, f.FailureCount, f.LastError)
				}
				continue
			}

			// Under budget: behaviour is deliberately unchanged (F-71). The
			// cycle aborts without persisting sync_state, so the next tick
			// retries from the same historyId. That is what lets a single
			// 503 or a momentarily full disk self-heal with no park, no
			// notification and no human involvement.
			if w.Logf != nil {
				w.Logf("failing: message %s (%d/%d): %s", id, f.FailureCount, w.Config.failureBudget(), f.LastError)
			}
			return w.failPoll(ctx, PollResult{StagedCount: staged, Parked: parked}, fmt.Errorf("gmailwatch: poll: process message %s: %w", id, perr))
		}

		staged += n
		// F-70's reset: a message that processed successfully starts clean.
		// The absence of a row IS a zero count, so there is no stale counter
		// to get wrong.
		if failedIDs[id] {
			if cerr := w.DB.ClearMessageFailure(ctx, id); cerr != nil {
				return w.failPoll(ctx, PollResult{StagedCount: staged, Parked: parked}, fmt.Errorf("gmailwatch: poll: clear failure state for %s: %w", id, cerr))
			}
			delete(failedIDs, id)
		}
	}

	if usedFallback {
		if hid, herr := w.currentHistoryID(ctx); herr == nil {
			newHistoryID = hid
		} else if reason, ok := isReauthError(herr); ok {
			return w.handleReauth(ctx, st, reason)
		}
		// Any other failure to establish a fresh baseline here is not
		// fatal: the next poll simply falls back again (safe, if
		// wasteful) rather than losing anything — F-30/G-1 (nothing
		// missed) outrank polling efficiency.
	}
	if newHistoryID != "" {
		st.HistoryID = newHistoryID
	}
	now := w.now()
	st.LastPollAt = &now
	// This poll reached Gmail successfully: any earlier re-auth condition
	// is resolved (F-16).
	st.LastAuthError = ""
	// ...and this cycle IS progress, so it ends any stall episode (F-81).
	// Note this runs even on a cycle that parked something: parking is
	// precisely what unwedged the poll.
	w.DB.ClearPollFailure(&st)

	if err := w.DB.SaveSyncState(ctx, st); err != nil {
		return w.failPoll(ctx, PollResult{StagedCount: staged, Parked: parked}, fmt.Errorf("gmailwatch: poll: persist sync_state: %w", err))
	}

	if staged > 0 && w.Notifier != nil {
		_ = notify.NotifyStaged(ctx, w.Notifier, staged)
	}

	return PollResult{StagedCount: staged, UsedFallback: usedFallback, Parked: parked, Retried: retried}, nil
}

// failPoll records a poll cycle that ended without persisting sync_state
// (F-81) and escalates once per stall episode (F-82). It returns cause
// unchanged, so call sites read `return w.failPoll(ctx, res, err)`.
//
// "Did not persist sync_state" is the trigger rather than "returned an
// error", which is why this is called from every non-progressing exit
// path — history.list failing, the fallback failing, SaveSyncState itself
// failing, and F-71's under-budget per-message abort.
func (w *Watcher) failPoll(ctx context.Context, res PollResult, cause error) (PollResult, error) {
	// A shutdown is not a stall. The same reasoning as consumesBudget: if
	// quitting the daemon counted as a failed poll, three restarts would
	// announce an outage that never happened.
	if !consumesBudget(cause) {
		return res, cause
	}

	st, escalate, err := w.DB.RecordPollFailure(ctx, cause.Error(), w.Config.pollFailureBudget(), w.now())
	if err != nil {
		// The stall bookkeeping failing must not replace the real error
		// with a less useful one.
		return res, errors.Join(cause, err)
	}
	if !escalate {
		return res, cause
	}

	// F-83: a cycle that already announced a park says nothing further. The
	// park notification is strictly more informative, and parking is what
	// unwedged the poll — so this stall is over before it started.
	//
	// The notify-once marker is rolled back rather than left set, so the
	// marker keeps meaning "the human has actually been told". Without
	// this, a cycle that parked one message and then aborted under budget
	// on another would consume the episode's one notification without ever
	// sending it, and a genuine ongoing stall would stay silent.
	if len(res.Parked) > 0 {
		st.PollStallNotifiedAt = nil
		if serr := w.DB.SaveSyncState(ctx, st); serr != nil {
			return res, errors.Join(cause, serr)
		}
		return res, cause
	}

	if w.Notifier != nil {
		since := w.now()
		if st.FirstPollFailureAt != nil {
			since = *st.FirstPollFailureAt
		}
		_ = w.Notifier.Notify(ctx, notify.PollStalled(st.ConsecutivePollFailures, since, st.LastPollError))
		res.StallNotified = true
	}
	if w.Logf != nil {
		w.Logf("stalled: %d consecutive polls have made no progress: %s", st.ConsecutivePollFailures, st.LastPollError)
	}
	return res, cause
}

// recordFailure charges one failure against id's budget and reports the
// resulting state plus whether this attempt is the one that parked it
// (F-70, F-72, F-76).
//
// The budget it applies is NOT simply the configured one. A message that has
// been parked before (ParkCount >= 1) is given an effective budget of 1, so
// a single failure re-parks it immediately and the poll continues. Without
// that, every automatic retry of a permanently broken message would cost
// another full failure_budget × poll_interval of stalled mailbox, on a
// schedule, forever — the exact behaviour parking exists to prevent. F-71's
// fail-the-poll path is a concession to transience, and a message that has
// already been parked has forfeited the presumption of transience.
func (w *Watcher) recordFailure(ctx context.Context, id string, cause error) (queue.MessageFailure, bool, error) {
	budget := w.Config.failureBudget()
	if cur, err := w.DB.GetMessageFailure(ctx, id); err != nil {
		return queue.MessageFailure{}, false, fmt.Errorf("gmailwatch: poll: read failure state for %s: %w", id, err)
	} else if cur != nil && cur.ParkCount >= 1 {
		budget = 1
	}

	f, newlyParked, err := w.DB.RecordMessageFailure(ctx, id, cause.Error(), budget, w.now(), w.Config.parkRetryCooldown())
	if err != nil {
		return queue.MessageFailure{}, false, fmt.Errorf("gmailwatch: poll: record failure for %s: %w", id, err)
	}
	return f, newlyParked, nil
}

// processMessage fetches one Gmail message's raw RFC 822 bytes and runs it
// through extract.ExtractMessage (F-20…F-25, including the F-30/L1 skip),
// with rules.Engine installed as the extractor's Gate so a denied document
// never becomes a queue row at all.
//
// The Gate placement is deliberate. Staging first and rejecting afterwards
// looks equivalent but is not: queue.StageItem's F-44 rejection memory
// makes a (gmail_message_id, sha256) pair permanently unstageable once
// rejected, so a document denied by a rule could never be recovered if the
// developer later added an allow rule for that vendor. That is a silent,
// permanent miss — precisely what G-1 forbids — and F-26's deny form says
// "never even queue" for this reason.
//
// Returns how many items ended up genuinely queued.
// forceReextract is true only for an id admitted from the retry set this
// cycle (F-78). It is threaded through as a parameter rather than stashed on
// the Watcher precisely so it cannot leak to a listed message: there is no
// state to forget to clear.
func (w *Watcher) processMessage(ctx context.Context, id string, forceReextract bool) (staged int, err error) {
	gm, err := w.Service.Users.Messages.Get(w.UserID, id).Format("raw").Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("gmailwatch: messages.get(%s): %w", id, err)
	}
	// F-11 watch scope, enforced here and nowhere else. format=raw still
	// populates LabelIds, so this costs no extra API call. Until v1.8 the
	// scope was delegated to history.list's labelId parameter, which both
	// let SENT and DRAFT adds through and dropped imported mail outright —
	// see watchscope.go.
	if !w.inScope(gm.LabelIds) {
		if w.Logf != nil {
			w.Logf("skip (scope): message %s labels %v outside watch scope %q", id, gm.LabelIds, w.effectiveWatch())
		}
		return 0, nil
	}
	raw, err := decodeRawMessage(gm.Raw)
	if err != nil {
		return 0, fmt.Errorf("gmailwatch: decode raw message %s: %w", id, err)
	}
	// format=raw does not populate Payload, so From/Subject come from the
	// raw headers themselves rather than gm.Payload.
	from, subject, _ := parseRawHeaders(raw)

	// Install the rules engine as the pre-insert gate for this message.
	var gateErr error
	w.Extractor.Gate = func(c extract.Candidate) (bool, string) {
		decision, derr := w.Rules.EvaluateAndRecord(ctx, rules.Document{
			GmailMessageID: id,
			From:           from,
			Subject:        subject,
			HasPDF:         c.IsPDF,
			HasImage:       c.IsImage,
		}, w.DB)
		if derr != nil {
			// Fail safe toward recall: if the decision cannot be recorded we
			// stage anyway and surface the error. Dropping a document because
			// of a logging failure would be a silent miss (G-1).
			gateErr = derr
			return true, ""
		}
		if decision.Decision != rules.DecisionQueued {
			return false, string(decision.Decision) + ": " + decision.Reason
		}
		return true, ""
	}
	defer func() { w.Extractor.Gate = nil }()

	res, err := w.Extractor.ExtractMessage(ctx, extract.Message{
		GmailMessageID: id,
		ThreadID:       gm.ThreadId,
		From:           from,
		Subject:        subject,
		InternalDate:   time.UnixMilli(gm.InternalDate).UTC(),
		Raw:            raw,
		ForceReextract: forceReextract,
	})
	if err != nil {
		return 0, fmt.Errorf("gmailwatch: extract message %s: %w", id, err)
	}
	if gateErr != nil {
		return 0, fmt.Errorf("gmailwatch: record rules decision for message %s: %w", id, gateErr)
	}
	if res.Skipped {
		// F-30 (L1): already recorded, never re-extracted. Nothing to do —
		// AC-10 is exactly this path taken on a replay.
		if w.Logf != nil {
			w.Logf("skip (L1): message %s already seen: %s", id, res.SkipReason)
		}
		return 0, nil
	}

	for _, d := range res.Denied {
		// F-26/F-27: denied and no-match-dropped documents never became
		// queue rows at all — the Gate refused them before StageItem. The
		// decision is already in decision_log; log it so the reason is
		// visible without reading the database (F-28, G-1).
		if w.Logf != nil {
			w.Logf("dropped: message %s document %q: %s", id, d.OrigFilename, d.Reason)
		}
	}
	staged += len(res.Items)
	return staged, nil
}

// decodeRawMessage decodes Gmail's base64url-encoded raw message field.
// Gmail's own documentation and observed behaviour disagree on padding
// across client libraries, so both the padded and unpadded url-safe
// alphabets are tried.
func decodeRawMessage(s string) ([]byte, error) {
	if b, err := base64.URLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("gmailwatch: decode base64url raw message: %w", err)
	}
	return b, nil
}

// parseRawHeaders extracts the From and Subject headers from a raw RFC 822
// message. Best-effort: extract's own MIME walk is authoritative for
// attachment parsing and never depends on this succeeding.
func parseRawHeaders(raw []byte) (from, subject string, err error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", "", fmt.Errorf("gmailwatch: parse raw headers: %w", err)
	}
	return m.Header.Get("From"), m.Header.Get("Subject"), nil
}
