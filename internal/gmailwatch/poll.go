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
		return PollResult{}, fmt.Errorf("gmailwatch: poll: %w", err)
	}

	var staged int
	for _, id := range msgIDs {
		n, perr := w.processMessage(ctx, id)
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
				if w.Logf != nil {
					w.Logf("skip (gone): message %s no longer exists in Gmail (404)", id)
				}
				continue
			}
			return PollResult{StagedCount: staged}, fmt.Errorf("gmailwatch: poll: process message %s: %w", id, perr)
		}
		staged += n
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
	now := time.Now().UTC()
	st.LastPollAt = &now
	// This poll reached Gmail successfully: any earlier re-auth condition
	// is resolved (F-16).
	st.LastAuthError = ""

	if err := w.DB.SaveSyncState(ctx, st); err != nil {
		return PollResult{StagedCount: staged}, fmt.Errorf("gmailwatch: poll: persist sync_state: %w", err)
	}

	if staged > 0 && w.Notifier != nil {
		_ = notify.NotifyStaged(ctx, w.Notifier, staged)
	}

	return PollResult{StagedCount: staged, UsedFallback: usedFallback}, nil
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
func (w *Watcher) processMessage(ctx context.Context, id string) (staged int, err error) {
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
