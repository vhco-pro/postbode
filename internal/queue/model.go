package queue

import "time"

// Status is an item's lifecycle state (F-41).
type Status string

// The F-41 lifecycle: staged -> approved -> uploaded, with terminal
// alternatives rejected, already_in_portal, failed, suppressed_peppol and
// duplicate_linked (the last ratified in spec v1.3, OQ-P1 — see doc.go).
const (
	StatusStaged           Status = "staged"
	StatusApproved         Status = "approved"
	StatusUploaded         Status = "uploaded"
	StatusRejected         Status = "rejected"
	StatusAlreadyInPortal  Status = "already_in_portal"
	StatusFailed           Status = "failed"
	StatusSuppressedPeppol Status = "suppressed_peppol"
	StatusDuplicateLinked  Status = "duplicate_linked"
)

// ActiveDedupStatuses are the statuses eligible to be matched by the L2
// SHA-256 check, and mirror exactly the predicate of the partial unique
// index on item.sha256 (spec §5.2).
var ActiveDedupStatuses = []Status{StatusStaged, StatusApproved, StatusUploaded, StatusAlreadyInPortal}

// Actor identifies who caused an item transition (F-41).
type Actor string

const (
	ActorHuman  Actor = "human"
	ActorDaemon Actor = "daemon"
)

// DedupLayer names which of the four dedup layers linked or flagged an item.
type DedupLayer string

const (
	DedupLayerL1 DedupLayer = "L1"
	DedupLayerL2 DedupLayer = "L2"
	DedupLayerL3 DedupLayer = "L3"
	DedupLayerL4 DedupLayer = "L4"
)

// Message mirrors the message entity (spec §5.2), owned by gmailwatch.
type Message struct {
	GmailMessageID    string
	ThreadID          string
	From              string
	Subject           string
	InternalDate      time.Time
	FirstSeenAt       time.Time
	AllDocsUploadedAt *time.Time
	LabeledAt         *time.Time
}

// Item mirrors the item entity (spec §5.2), owned by queue.
type Item struct {
	ID                     int64
	GmailMessageID         string
	SpoolPath              string
	OrigFilename           string
	ProposedFilename       string
	MimeType               string
	SizeBytes              int64
	SHA256                 string
	IdentityKey            string
	IdentityConfidence     string
	IdentitySource         string
	Status                 Status
	NeedsManualHandling    bool
	LowConfidence          bool
	PossibleDuplicate      bool
	ProbablyAlreadyHandled bool
	UnsupportedType        bool
	LinkedItemID           *int64
	DedupLayer             DedupLayer
	StagedAt               time.Time
	ApprovedAt             *time.Time
	UploadedAt             *time.Time
	UUID                   string
	AmountOfPages          int
	VerifiedAt             *time.Time
	FailedAt               *time.Time
	LastError              string
	RetryCount             int
	NextRetryAt            *time.Time
	ClaimedAt              *time.Time
	// FirstFailedAt is a Phase 9 addition (F-51): the moment this item's
	// upload first failed with a retryable error, set once and never
	// overwritten. It anchors the 24h give-up window independently of
	// retry_count, and survives a daemon restart. Nil until the first
	// retryable failure.
	FirstFailedAt *time.Time
}

// MessageFailure is one message's consecutive-failure and park state
// (F-70…F-79), owned by gmailwatch. It is the inbound-side counterpart to
// F-51's outbound upload retry budget on Item, and deliberately reuses that
// vocabulary rather than inventing a second one:
//
//	Item (F-51, uploads)   MessageFailure (F-70, inbound)
//	FirstFailedAt          FirstFailedAt   anchors the episode, set once
//	RetryCount             RetryCount      attempts spent against the bound
//	NextRetryAt            NextRetryAt     when the next attempt is due
//	LastError              LastError       truncated, redacted
//
// A row exists only while a message is failing or parked: successful
// processing deletes it outright, which is how FailureCount resets. See
// decisions/ADR-004-park-and-continue-with-bounded-auto-retry.md.
type MessageFailure struct {
	GmailMessageID string
	// FailureCount is consecutive failures in the current episode. It
	// reaches Config.FailureBudget exactly once per park.
	FailureCount  int
	FirstFailedAt time.Time
	LastFailedAt  time.Time
	// LastError is truncated to MaxErrorLen and carries no message body or
	// attachment content (F-65, NF-17).
	LastError string
	// ParkedAt is nil while the message is merely failing. Non-nil means the
	// poll no longer stops for it.
	ParkedAt *time.Time
	// ParkCount is how many times this message has been parked, ever. It is
	// never reset by a retry: >= 1 is what puts a message on F-76's
	// effective budget of 1, so a retry can never re-wedge the poll.
	ParkCount int
	// RetryCount is automatic attempts spent against ParkRetryAttempts.
	// Never reset by a retry, for the same reason as ParkCount.
	RetryCount int
	// NextRetryAt is when the next automatic attempt becomes due. Nil means
	// the automatic attempts are exhausted: the message stays parked and
	// stays reported (F-79), and only `postbode retry` can schedule another.
	NextRetryAt *time.Time
	// NotifiedAt is F-74's notify-once marker, persisted so a restart does
	// not re-notify about an already-known park.
	NotifiedAt *time.Time
}

// Parked reports whether this message has been parked, i.e. whether the poll
// loop now continues past it rather than aborting the cycle.
func (f MessageFailure) Parked() bool { return f.ParkedAt != nil }

// MaxErrorLen bounds every persisted error string (NF-17), so a pathological
// error — a server echoing a whole request back, say — cannot bloat the
// database or overflow a macOS notification.
const MaxErrorLen = 500

// TruncateError clamps s to MaxErrorLen runes, marking that it was cut so a
// reader never mistakes a truncated error for the whole story.
func TruncateError(s string) string {
	r := []rune(s)
	if len(r) <= MaxErrorLen {
		return s
	}
	return string(r[:MaxErrorLen]) + "… (truncated)"
}

// VendorTeaching mirrors the vendor_teaching entity (spec §5.2), owned by
// queue. Populated from Phase 12 onward (L4); the table exists from Phase 4.
type VendorTeaching struct {
	VendorDomain string
	IdentityKey  string
	Reason       string
	MarkedAt     time.Time
	Note         string
}

// SyncState mirrors the sync_state entity (spec §5.2), owned by gmailwatch.
// The table exists from Phase 4; gmailwatch (Phase 7) populates it.
type SyncState struct {
	HistoryID        string
	LastPollAt       *time.Time
	LabelIDSubmitted string
	TokenIssuedAt    *time.Time
	// LastAuthError is a Phase 7 addition (F-16/F-17): non-empty exactly
	// when the most recent poll could not reach Gmail because
	// re-authentication is needed (an oauth2 RFC 6749 error code such as
	// "invalid_grant"). Cleared on the next poll that reaches Gmail
	// successfully, or when a fresh token is recorded via
	// gmailwatch.Watcher.RecordTokenIssued. This is how F-17's "re-auth
	// needed" flag is exposed through sync_state for Phase 10 to print.
	LastAuthError string

	// ConsecutivePollFailures counts poll cycles that ended WITHOUT
	// persisting sync_state (F-81). That predicate — not "returned an
	// error" — is what makes one counter cover history.list failing, the
	// F-13 fallback failing, SaveSyncState itself failing, and F-71's
	// under-budget per-message abort. Reset to zero by any poll that
	// persists.
	ConsecutivePollFailures int
	// FirstPollFailureAt anchors the current stall episode; nil when
	// healthy.
	FirstPollFailureAt *time.Time
	// LastPollError is the truncated, redacted error from the most recent
	// failed poll.
	LastPollError string
	// PollStallNotifiedAt is F-82's notify-once marker for the current
	// episode, persisted so a restart mid-stall does not re-notify. Cleared
	// by the poll that ends the episode, so a LATER stall is a new episode
	// and does notify again.
	PollStallNotifiedAt *time.Time
}

// PollHealthy reports whether the daemon is making progress: no consecutive
// poll failures recorded. Used by `postbode status` to state poll health in
// words rather than leaving the reader to subtract timestamps (F-84).
func (s SyncState) PollHealthy() bool { return s.ConsecutivePollFailures == 0 }

// DecisionLogEntry mirrors the decision_log entity (spec §5.2), owned by
// rules. The table exists from Phase 4; rules (Phase 6) populates it.
type DecisionLogEntry struct {
	ID               int64
	GmailMessageID   string
	Decision         string
	MatchedRuleIndex *int
	Reason           string
	At               time.Time
}

// Transition is one row of the item_transition audit log. This table is a
// Phase 4 addition beyond the five entities spec §5.2 names verbatim: F-41
// requires "every transition logged with timestamp and actor", and §5.2
// names no entity to hold that log, so item_transition supplies it without
// altering any of the five named entities' column lists.
type Transition struct {
	ID     int64
	ItemID int64
	From   Status // empty for the initial staged/duplicate_linked/suppressed_peppol insert
	To     Status
	Actor  Actor
	At     time.Time
}

// NewMessage is the input to RecordMessageIfNew.
type NewMessage = Message

// NewItem is the input to StageItem — everything the extractor/rules
// pipeline knows about one candidate document before dedup runs.
type NewItem struct {
	GmailMessageID      string
	SpoolPath           string
	OrigFilename        string
	ProposedFilename    string
	MimeType            string
	SizeBytes           int64
	SHA256              string
	IdentityKey         string
	IdentityConfidence  string
	IdentitySource      string
	NeedsManualHandling bool
	LowConfidence       bool
	UnsupportedType     bool
	// VendorDomain is the sender's domain (internal/dedup.VendorDomain),
	// used only as StageItem's L4 (F-34/F-35) match key against
	// vendor_teaching — it is not persisted as its own column. Empty means
	// "no domain known", which simply disables L4 matching for this item;
	// it never blocks staging.
	VendorDomain string
	// SuppressedPeppol is the caller's F-36 known-Peppol glob match
	// (internal/dedup.MatchesKnownPeppol against config's
	// vendors.known_peppol), computed before StageItem runs because it is
	// config-declared, not something queue itself has any business
	// evaluating. When true the item still stages — in status
	// suppressed_peppol — never dropped (AC-14).
	SuppressedPeppol bool
}

// StageResult is the outcome of StageItem.
type StageResult struct {
	// ItemID is the id of the row inserted. Zero when Skipped is true.
	ItemID int64
	// Status is the status the item was inserted with: staged or
	// duplicate_linked (L2 match). Empty when Skipped is true.
	Status Status
	// DedupLayer is "L2" when this item was linked to an earlier
	// byte-identical item, empty otherwise.
	DedupLayer DedupLayer
	// LinkedItemID is set when DedupLayer == DedupLayerL2.
	LinkedItemID *int64
	// Skipped is true when the item was not staged at all because F-44
	// rejection memory matched (gmail_message_id, sha256).
	Skipped bool
	// SkipReason explains why, when Skipped is true.
	SkipReason string
}
