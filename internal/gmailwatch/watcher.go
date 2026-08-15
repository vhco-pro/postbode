package gmailwatch

import (
	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/rules"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
)

// Config is the subset of Postbode's configuration (spec §6.5's gmail.*
// keys) Watcher needs. The daemon (cmd/postboded, out of this package's
// scope) maps internal/config.Config.Gmail onto this.
type Config struct {
	// Watch is F-11's gmail.watch value: WatchAll (the default, and what an
	// empty or unrecognised value resolves to) or WatchInbox, both
	// case-insensitive. See watchscope.go for what each scope admits and why
	// the default is no longer INBOX-only.
	Watch string
	// QueryWindowDays bounds the F-13 fallback query (default 30 when <= 0).
	QueryWindowDays int
	// SubmittedLabel is the exact full label name F-15 resolves (default
	// SubmittedLabelName when empty).
	SubmittedLabel string
}

// Watcher is Postbode's Gmail poller (spec §3.2, F-10…F-19): incremental
// history sync (F-12) with a windowed fallback (F-13), rules-gated staging
// through extract and rules, and re-auth handling that never stops polling
// (F-16).
//
// # Why the rules gate lives here, applied AFTER extract, not before
//
// Phase 5's extract.ExtractMessage already calls queue.StageItem
// unconditionally for every extracted candidate document (F-20…F-25) — it
// has no rules dependency and shipped that way, tested and green. Rather
// than reopen extract to inject a pre-staging decision hook, Watcher
// evaluates rules.Engine.EvaluateAndRecord immediately after ExtractMessage
// returns, once per freshly staged (neither duplicate-linked nor
// rejection-memory-skipped) item, and — for anything the engine does not
// decide DecisionQueued — immediately transitions that item
// staged -> rejected (queue.DB.Reject, actor daemon) before any caller
// (the notifier, the review UI) ever observes it. The net effect matches
// F-26/F-27's "denied/dropped documents never queue" behaviourally, at the
// cost of a transient staged row a human never sees; the decision_log entry
// F-28 requires is written first either way, via EvaluateAndRecord. See
// processMessage in poll.go.
type Watcher struct {
	Service   *gmail.Service
	DB        *queue.DB
	Extractor *extract.Extractor
	Rules     *rules.Engine
	Notifier  notify.Notifier
	// UserID is the Gmail user id Postbode operates as — conventionally
	// "me" for the single OAuth token this single-user daemon holds
	// (spec §1: one user, one mailbox).
	UserID string
	Config Config

	// OAuthConfig, when set, lets F-16's re-auth notification embed a real,
	// ready-to-open consent URL rather than a generic instruction. The
	// production daemon sets this to the same *oauth2.Config
	// AuthenticateInteractive uses.
	OAuthConfig *oauth2.Config

	// Logf, when set, receives one line per notable poll event — most
	// importantly "skip (L1): ..." on every F-30 replay skip (AC-10: "the
	// second pass writes a skip (L1) log line"). Postbode's durable audit
	// trail is decision_log/item_transition (SQL, queried by `postbode
	// log`); Logf is deliberately just an optional line sink, not a second
	// logging subsystem — nil is a valid, silent default.
	Logf func(format string, args ...any)
}

// PollResult summarizes the outcome of one Poll call.
type PollResult struct {
	// StagedCount is how many items ended DecisionQueued this poll — the
	// count F-45's staging notification reports.
	StagedCount int
	// UsedFallback is true when this poll used the F-13 windowed fallback
	// instead of F-12's incremental history sync.
	UsedFallback bool
	// ReauthNeeded is true when this poll could not reach Gmail because
	// re-authentication is required (F-16). Poll never treats this as a
	// fatal error — see handleReauth in reauth.go.
	ReauthNeeded bool
}
