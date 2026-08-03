package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

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
	// Warnings holds non-fatal MIME parse/decode problems encountered
	// while walking. Every part that did not become an Item is accounted
	// for by either an Item (staged, possibly unsupported_type) or a
	// Warning here — nothing disappears without a trace (G-1).
	Warnings []error
}

// Extractor turns raw Gmail messages into queue items (F-20…F-25). It
// never duplicates queue's own dedup or staging logic — every write goes
// through queue.DB's public API (StageItem, RecordMessageIfNew).
type Extractor struct {
	spooler *Spooler
	db      *queue.DB
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
	seen, err := e.db.MessageSeen(ctx, msg.GmailMessageID)
	if err != nil {
		return Result{}, fmt.Errorf("extract: ExtractMessage(%s): check L1: %w", msg.GmailMessageID, err)
	}
	if seen {
		return Result{Skipped: true, SkipReason: "L1 message-id already seen"}, nil
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
	if alreadySeen {
		// Lost a race with a concurrent extraction of the same message:
		// clean up what this call just spooled and defer to whichever
		// call recorded the message first.
		for _, s := range spooled {
			_ = e.spooler.Remove(s.path)
		}
		return Result{Skipped: true, SkipReason: "L1 message-id already seen (concurrent)"}, nil
	}

	items := make([]ItemResult, 0, len(spooled))
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
		})
		if serr != nil {
			return Result{Items: items, Warnings: warnings}, fmt.Errorf("extract: ExtractMessage(%s): stage item %q: %w", msg.GmailMessageID, s.filename, serr)
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

	return Result{Items: items, Warnings: warnings}, nil
}
