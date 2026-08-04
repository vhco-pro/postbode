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
}

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
