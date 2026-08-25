package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/vhco-pro/postbode/internal/dedup"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Message is everything ExtractMessage needs about one Gmail message: the
// full raw RFC 822 bytes, plus the metadata queue.DB.RecordMessageIfNew
// (L1) requires to record it. Fetching this from the Gmail API is
// gmailwatch's job (Phase 7); extract only ever consumes it.
type Message struct {
	GmailMessageID string
	ThreadID       string
	From           string
	Subject        string
	InternalDate   time.Time
	Raw            []byte

	// ForceReextract bypasses the L1 message-id skip (F-30) for this one
	// call, and NOTHING else. L2 (SHA-256), L3, L4 and F-44's rejection
	// memory all remain fully in force, so a re-extraction cannot produce a
	// second uploadable copy of a document already in the queue: a
	// byte-identical document links as duplicate_linked exactly as AC-11
	// requires, and a previously rejected (message, sha256) pair stays
	// unstageable.
	//
	// It exists for exactly one caller: gmailwatch admitting a parked
	// message for a retry (F-78, decisions/ADR-005-retry-scoped-l1-bypass.md).
	//
	// Why it has to exist. ExtractMessage records the message as seen
	// BEFORE staging its documents (see the spool-before-record fail-safe
	// below), so a failure in that window leaves a message that is recorded
	// but has no queue rows. Without this flag every later retry would hit
	// the L1 check, return Skipped, log "skip (L1)", report success and
	// clear the park — no error, no log line, nothing to see, and the
	// invoice gone forever. That is the exact silent miss G-1 forbids, and
	// it passes casual inspection completely.
	//
	// Set it ONLY for an id admitted from the retry set, never for a listed
	// id, never globally, never stickily — F-30's replay protection is what
	// stops an F-13 fallback resync from re-extracting the whole mailbox.
	ForceReextract bool
}

// ItemResult describes one extracted candidate document and the queue
// item it produced.
type ItemResult struct {
	OrigFilename     string
	ProposedFilename string
	DeclaredMIME     string
	SniffedMIME      string
	SHA256           string
	SpoolPath        string
	Stage            queue.StageResult
}

// Candidate is one extracted document offered to a Gate before any queue
// row exists for it.
type Candidate struct {
	OrigFilename string
	DeclaredMIME string
	SniffedMIME  string
	SHA256       string
	IsPDF        bool
	IsImage      bool
}

// Gate decides whether a candidate becomes a queue row at all. It runs
// AFTER spooling (so the fail-safe ordering in ExtractMessage is intact)
// but BEFORE StageItem.
//
// This exists because F-26's deny form means "never even queue", and
// staging-then-rejecting is not the same thing: queue.StageItem's F-44
// rejection memory makes a (gmail_message_id, sha256) pair permanently
// unstageable once rejected. A document denied by a rule would therefore
// be unrecoverable if the developer later added an allow rule for that
// vendor — a silent, permanent miss, which is exactly what G-1 forbids.
// Gating before the insert keeps a deny decision reversible: nothing is
// written, so a config change genuinely takes effect on the next resync.
//
// A nil Gate stages everything.
type Gate func(Candidate) (stage bool, reason string)

// Denied records a candidate a Gate refused, so nothing disappears
// without a trace (G-1). Its spool file is removed.
type Denied struct {
	Candidate
	Reason string
}

// Result is the outcome of ExtractMessage.
type Result struct {
	// Skipped is true when the message had already been recorded (L1) —
	// no MIME walk, no spool I/O and no queue writes happened at all.
	Skipped    bool
	SkipReason string
	// Items holds one entry per extracted candidate document (F-21): a
	// message yielding N PDFs yields N entries here, all sharing the one
	// GmailMessageID.
	Items []ItemResult
	// Denied holds candidates the Gate refused. They have no queue row and
	// no spool file, so a later config change can recover them (F-26).
	Denied []Denied
	// Warnings holds non-fatal MIME parse/decode problems encountered
	// while walking. Every part that did not become an Item is accounted
	// for by either an Item (staged, possibly unsupported_type), a Denied
	// entry, or a Warning here — nothing disappears without a trace (G-1).
	Warnings []error
}

// Extractor turns raw Gmail messages into queue items (F-20…F-25). It
// never duplicates queue's own dedup or staging logic — every write goes
// through queue.DB's public API (StageItem, RecordMessageIfNew).
type Extractor struct {
	spooler *Spooler
	db      *queue.DB
	// Gate, when non-nil, decides per candidate whether a queue row is
	// created at all. See the Gate doc for why this must happen before
	// StageItem rather than as a reject afterwards.
	Gate Gate
	// KnownPeppolGlobs is config's vendors.known_peppol (F-36) — the
	// sender-address glob list a document must NOT be automatically
	// uploaded from without an explicit UI override. Extractor only
	// evaluates the match (internal/dedup.MatchesKnownPeppol) and passes
	// the result to StageItem; it never reads config itself, so this
	// package stays free of a dependency on internal/config. Nil/empty
	// means no vendor is configured as known-Peppol.
	KnownPeppolGlobs []string
}

// New builds an Extractor spooling into spoolDir. Production wires
// DefaultSpoolDir(); tests always pass a t.TempDir() (F-24: the spool
// base directory is injectable, never a real home directory in tests).
func New(spoolDir string, db *queue.DB) *Extractor {
	return &Extractor{spooler: NewSpooler(spoolDir), db: db}
}

// NewWithSpooler builds an Extractor around a caller-supplied *Spooler —
// used by tests that need to inject a failing writer to exercise the
// spec §8 disk-full fail-safe.
func NewWithSpooler(spooler *Spooler, db *queue.DB) *Extractor {
	return &Extractor{spooler: spooler, db: db}
}

// ExtractMessage is the Phase 5 entry point: walk msg's MIME tree, spool
// every PDF candidate, sniff and classify each one, and stage one queue
// item per candidate. See the package doc for the disk-full fail-safe
// ordering this method enforces.
func (e *Extractor) ExtractMessage(ctx context.Context, msg Message) (Result, error) {
	if msg.GmailMessageID == "" {
		return Result{}, fmt.Errorf("extract: ExtractMessage: gmail message id is required")
	}

	// Cheap early L1 skip (F-30): no MIME walk, no spool I/O and no queue
	// writes for a message already recorded. queue.DB.RecordMessageIfNew's
	// own doc comment is explicit that a message recorded here must never
	// be re-extracted, regardless of history replay, restart or resync.
	// ForceReextract skips this check, and only this check — see its doc
	// comment on Message for why a retried message MUST get past L1.
	if !msg.ForceReextract {
		seen, err := e.db.MessageSeen(ctx, msg.GmailMessageID)
		if err != nil {
			return Result{}, fmt.Errorf("extract: ExtractMessage(%s): check L1: %w", msg.GmailMessageID, err)
		}
		if seen {
			return Result{Skipped: true, SkipReason: "L1 message-id already seen"}, nil
		}
	}

	candidates, warnings := walkMIME(msg.Raw)

	// --- Fail-safe on disk-full (spec §8) -----------------------------
	// Every candidate must spool successfully BEFORE the message is
	// recorded as seen and BEFORE any queue row is committed. A failure
	// partway through aborts the whole message: any spool files already
	// written for it are best-effort removed, the message is NEVER marked
	// seen, and no queue row is ever committed — the caller's next poll
	// will see this message again and retry it from scratch, rather than
	// permanently losing it.
	type spooledCandidate struct {
		candidate
		path string
		sha  string
	}
	spooled := make([]spooledCandidate, 0, len(candidates))
	for _, c := range candidates {
		sum := sha256.Sum256(c.data)
		sha := hex.EncodeToString(sum[:])
		path, werr := e.spooler.Write(sha+".pdf", c.data)
		if werr != nil {
			for _, s := range spooled {
				_ = e.spooler.Remove(s.path)
			}
			return Result{}, fmt.Errorf("extract: ExtractMessage(%s): spool write failed, message NOT marked seen, no queue rows committed: %w", msg.GmailMessageID, werr)
		}
		spooled = append(spooled, spooledCandidate{candidate: c, path: path, sha: sha})
	}
	// --------------------------------------------------------------------

	// Only now record the message as seen (L1) — every candidate is
	// safely on disk, so this message will never be re-extracted whether
	// or not staging below succeeds.
	alreadySeen, err := e.db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: msg.GmailMessageID,
		ThreadID:       msg.ThreadID,
		From:           msg.From,
		Subject:        msg.Subject,
		InternalDate:   msg.InternalDate,
	})
	if err != nil {
		for _, s := range spooled {
			_ = e.spooler.Remove(s.path)
		}
		return Result{}, fmt.Errorf("extract: ExtractMessage(%s): record message: %w", msg.GmailMessageID, err)
	}
	if alreadySeen && !msg.ForceReextract {
		// Lost a race with a concurrent extraction of the same message:
		// clean up what this call just spooled and defer to whichever
		// call recorded the message first.
		//
		// A forced re-extraction reaches here on EVERY attempt — the row it
		// is "racing" is the one its own earlier, failed attempt wrote — so
		// this branch must not fire for it, or the bypass above would be
		// undone three lines later and the silent miss would survive.
		for _, s := range spooled {
			_ = e.spooler.Remove(s.path)
		}
		return Result{Skipped: true, SkipReason: "L1 message-id already seen (concurrent)"}, nil
	}

	// L4/F-36 inputs computed once per message, not per candidate document
	// — a message has exactly one sender, so vendor domain and the
	// known-Peppol match are the same for every PDF it carries.
	vendorDomain := dedup.VendorDomain(msg.From)
	suppressedPeppol := dedup.MatchesKnownPeppol(msg.From, e.KnownPeppolGlobs)

	items := make([]ItemResult, 0, len(spooled))
	var denied []Denied
	for _, s := range spooled {
		sniffed := SniffMIME(s.data) // F-25: real-content sniff, not the declared type
		unsupported := sniffed == ""
		mimeForItem := sniffed
		if unsupported {
			// Record what it actually sniffs to (even if not accepted) so
			// the reason is visible in the UI/log rather than just "no".
			mimeForItem = http.DetectContentType(s.data)
		}
		needsManual := !unsupported && sniffed == "application/pdf" && IsEncryptedPDF(s.data) // F-22

		proposed := ProposedFilename(msg.From, msg.InternalDate, s.filename) // F-23

		// F-26 deny means "never even queue". Ask the Gate before any row
		// exists: staging then rejecting would burn this (message_id,
		// sha256) into F-44 rejection memory permanently, so a later allow
		// rule could never recover the document.
		if e.Gate != nil {
			cand := Candidate{
				OrigFilename: s.filename,
				DeclaredMIME: s.declaredMIME,
				SniffedMIME:  mimeForItem,
				SHA256:       s.sha,
				IsPDF:        sniffed == "application/pdf",
				IsImage:      sniffed == "image/jpeg",
			}
			if stage, reason := e.Gate(cand); !stage {
				_ = e.spooler.Remove(s.path)
				denied = append(denied, Denied{Candidate: cand, Reason: reason})
				continue
			}
		}

		// L3 (F-32): identity is parsed per document, not per message —
		// each PDF in a multi-attachment message has its own filename and
		// (usually) its own invoice number/date/amount, even though they
		// share one subject line.
		identity := dedup.ParseIdentity(vendorDomain, s.filename, msg.Subject)

		stageRes, serr := e.db.StageItem(ctx, queue.NewItem{
			GmailMessageID:      msg.GmailMessageID,
			SpoolPath:           s.path,
			OrigFilename:        s.filename,
			ProposedFilename:    proposed,
			MimeType:            mimeForItem,
			SizeBytes:           int64(len(s.data)),
			SHA256:              s.sha,
			NeedsManualHandling: needsManual,
			UnsupportedType:     unsupported,
			IdentityKey:         identity.Key,
			IdentityConfidence:  identity.Confidence,
			IdentitySource:      identity.Source,
			VendorDomain:        vendorDomain,
			SuppressedPeppol:    suppressedPeppol,
		})
		if serr != nil {
			return Result{Items: items, Denied: denied, Warnings: warnings}, fmt.Errorf("extract: ExtractMessage(%s): stage item %q: %w", msg.GmailMessageID, s.filename, serr)
		}

		items = append(items, ItemResult{
			OrigFilename:     s.filename,
			ProposedFilename: proposed,
			DeclaredMIME:     s.declaredMIME,
			SniffedMIME:      mimeForItem,
			SHA256:           s.sha,
			SpoolPath:        s.path,
			Stage:            stageRes,
		})
	}

	return Result{Items: items, Denied: denied, Warnings: warnings}, nil
}
