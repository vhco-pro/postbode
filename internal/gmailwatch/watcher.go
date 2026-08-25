package gmailwatch

import (
	"time"

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

	// The four F-87 resilience bounds. Each follows the same "<= 0 means
	// the default" convention QueryWindowDays already uses, so a zero-value
	// Config is a working Config — which matters because several tests
	// construct one directly.
	//
	// FailureBudget is how many consecutive failures one message may cost
	// the whole poll before it is parked (F-70, F-72). Default
	// defaultFailureBudget.
	FailureBudget int
	// ParkRetryCooldown is the base interval before a parked message is
	// automatically retried, doubling per attempt (F-75). Default
	// defaultParkRetryCooldown.
	ParkRetryCooldown time.Duration
	// ParkRetryAttempts bounds those automatic retries (F-75). Default
	// defaultParkRetryAttempts.
	ParkRetryAttempts int
	// PollFailureBudget is how many consecutive non-progressing poll cycles
	// pass before the daemon escalates (F-81, F-82). Default
	// defaultPollFailureBudget.
	PollFailureBudget int
}

// The F-87 defaults, applied when a Config field is <= 0. They intentionally
// match internal/config's Default() — a Watcher built by hand in a test and
// one built from a real config file must behave the same.
const (
	defaultFailureBudget     = 3
	defaultParkRetryCooldown = 6 * time.Hour
	defaultParkRetryAttempts = 3
	defaultPollFailureBudget = 3
)

// now is the Watcher's clock, nil-safe.
func (w *Watcher) now() time.Time {
	if w.Clock != nil {
		return w.Clock().UTC()
	}
	return time.Now().UTC()
}

func (c Config) failureBudget() int {
	if c.FailureBudget <= 0 {
		return defaultFailureBudget
	}
	return c.FailureBudget
}

func (c Config) parkRetryCooldown() time.Duration {
	if c.ParkRetryCooldown <= 0 {
		return defaultParkRetryCooldown
	}
	return c.ParkRetryCooldown
}

func (c Config) parkRetryAttempts() int {
	if c.ParkRetryAttempts <= 0 {
		return defaultParkRetryAttempts
	}
	return c.ParkRetryAttempts
}

func (c Config) pollFailureBudget() int {
	if c.PollFailureBudget <= 0 {
		return defaultPollFailureBudget
	}
	return c.PollFailureBudget
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

	// Clock overrides time.Now().UTC() for the F-75 cooldown and retry-due
	// computations, so those tests never sleep on wall-clock hours. Nil
	// means the real clock. Same shape and same reason as uploader.Clock.
	Clock func() time.Time

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
	// Parked lists the message ids this cycle parked for the FIRST time
	// (F-72), i.e. exactly the ids that raised a park notification. A cycle
	// that merely re-parks an already-parked message contributes nothing
	// here, because nothing new was announced.
	Parked []string
	// Retried lists the parked message ids this cycle admitted for another
	// attempt (F-75, F-77), whether the attempt was due automatically or
	// forced by `postbode retry`.
	Retried []string
	// StallNotified is true when this cycle raised F-82's "the daemon is
	// alive but not making progress" notification.
	StallNotified bool
}
