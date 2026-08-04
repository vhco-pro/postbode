// Package rules implements Postbode's config-driven, first-match-wins
// document classifier (spec §3.4 F-26…F-29, F-47; PRD §6.3).
//
// Evaluation happens once per extracted document (not once per email): a
// message with two PDF attachments is evaluated twice, and each document
// may land on a different decision. Recall is favoured over precision (F-27)
// — the review queue, not the rules engine, is where precision happens — so
// the engine only ever *denies* a document when a rule explicitly says so;
// everything else that looks even faintly invoice-shaped is queued.
package rules

import (
	"context"
	"path"
	"strings"
	"time"

	"github.com/vhco-pro/postbode/internal/config"
	"github.com/vhco-pro/postbode/internal/queue"
)

// Decision is the outcome of evaluating one document against the ruleset
// (F-28).
type Decision string

const (
	// DecisionQueued means the document is staged into the review queue.
	DecisionQueued Decision = "queued"
	// DecisionDenied means an explicit deny rule matched: never queued.
	DecisionDenied Decision = "denied"
	// DecisionDropped means no rule matched and the default heuristic also
	// found no reason to queue it.
	DecisionDropped Decision = "no-match-dropped"
)

// DefaultKeywords are the mandatory invoice-ish keywords for the F-27
// default heuristic, matched case-insensitively against subject and body.
// A caller MAY extend this list but the engine always includes at least
// these — narrowing them would silently reduce recall (G-1).
var DefaultKeywords = []string{"factuur", "invoice", "receipt", "creditnota", "rekening"}

// Document is everything the engine needs to classify one extracted
// document. One Gmail message with N PDF attachments produces N Documents,
// all sharing GmailMessageID (F-21).
type Document struct {
	GmailMessageID  string
	From            string
	Subject         string
	Body            string
	HasPDF          bool
	HasImage        bool
	ListUnsubscribe bool
}

// Result is the outcome of Evaluate for one Document.
type Result struct {
	Decision Decision
	// MatchedRuleIndex is the 0-based index into the configured rules list
	// that decided this document, or nil when the default heuristic (or the
	// drop path) decided it.
	MatchedRuleIndex *int
	// Reason is a short, human-readable explanation for the decision log
	// (F-28).
	Reason string
	// LowConfidence is true whenever no explicit rule matched — a
	// keyword-only or attachment-only default-path match, or the drop path
	// (F-47).
	LowConfidence bool
}

// Recorder persists one decision_log row per document decision (F-28). Its
// signature mirrors queue.DecisionLogEntry exactly so a queue.DB-backed
// implementation is a direct field-for-field mapping; Engine itself has no
// dependency on *queue.DB.
type Recorder interface {
	RecordDecision(ctx context.Context, entry queue.DecisionLogEntry) error
}

// Engine evaluates documents against a fixed, pre-validated ruleset.
type Engine struct {
	rules    []config.Rule
	keywords []string
}

// New builds an Engine from a validated config.Config (see config.Load).
// The engine trusts that cfg.Rules already passed config.Load's glob and
// shape validation (F-29) — Evaluate never returns an error.
func New(cfg config.Config) *Engine {
	return &Engine{
		rules:    cfg.Rules,
		keywords: DefaultKeywords,
	}
}

// Evaluate classifies one document, first-match-wins over the configured
// rules, falling back to the F-27 default heuristic when nothing matches.
func (e *Engine) Evaluate(doc Document) Result {
	for i, r := range e.rules {
		switch {
		case r.Allow != nil && matches(r.Allow, doc):
			idx := i
			return Result{
				Decision:         DecisionQueued,
				MatchedRuleIndex: &idx,
				Reason:           "matched allow rule",
			}
		case r.Deny != nil && matches(r.Deny, doc):
			idx := i
			return Result{
				Decision:         DecisionDenied,
				MatchedRuleIndex: &idx,
				Reason:           "matched deny rule",
			}
		}
	}
	return e.defaultDecide(doc)
}

// EvaluateAndRecord evaluates doc and writes exactly one decision_log row
// via rec before returning the result (F-28).
func (e *Engine) EvaluateAndRecord(ctx context.Context, doc Document, rec Recorder) (Result, error) {
	res := e.Evaluate(doc)
	entry := queue.DecisionLogEntry{
		GmailMessageID:   doc.GmailMessageID,
		Decision:         string(res.Decision),
		MatchedRuleIndex: res.MatchedRuleIndex,
		Reason:           res.Reason,
		At:               time.Now().UTC(),
	}
	if err := rec.RecordDecision(ctx, entry); err != nil {
		return res, err
	}
	return res, nil
}

// defaultDecide implements F-27: queue_if(pdf_attachment || image_attachment
// || invoice_keywords), recall over precision. Every path through here is
// low confidence (F-47) because, by construction, no explicit rule matched.
func (e *Engine) defaultDecide(doc Document) Result {
	if doc.HasPDF {
		return Result{Decision: DecisionQueued, Reason: "default: pdf attachment present", LowConfidence: true}
	}
	if doc.HasImage {
		return Result{Decision: DecisionQueued, Reason: "default: image attachment present", LowConfidence: true}
	}
	if kw, ok := matchedKeyword(doc, e.keywords); ok {
		return Result{Decision: DecisionQueued, Reason: "default: invoice keyword match: " + kw, LowConfidence: true}
	}
	return Result{Decision: DecisionDropped, Reason: "default: no attachment and no invoice keyword", LowConfidence: true}
}

func matchedKeyword(doc Document, keywords []string) (string, bool) {
	subject := strings.ToLower(doc.Subject)
	body := strings.ToLower(doc.Body)
	for _, kw := range keywords {
		lkw := strings.ToLower(kw)
		if strings.Contains(subject, lkw) || strings.Contains(body, lkw) {
			return kw, true
		}
	}
	return "", false
}

// matches reports whether every set dimension of m holds for doc. An unset
// dimension (zero value) is not considered — it neither helps nor hinders a
// match.
func matches(m *config.Matcher, doc Document) bool {
	if m.From != "" {
		ok, err := path.Match(m.From, doc.From)
		if err != nil || !ok {
			return false
		}
	}
	if m.Subject != "" {
		if !strings.Contains(strings.ToLower(doc.Subject), strings.ToLower(m.Subject)) {
			return false
		}
	}
	if m.Has != "" {
		if !hasAttachment(doc, m.Has) {
			return false
		}
	}
	if m.HasNo != "" {
		if hasAttachment(doc, m.HasNo) {
			return false
		}
	}
	if m.ListUnsubscribe != nil {
		if doc.ListUnsubscribe != *m.ListUnsubscribe {
			return false
		}
	}
	return true
}

func hasAttachment(doc Document, kind config.AttachmentKind) bool {
	switch kind {
	case config.AttachmentPDF:
		return doc.HasPDF
	case config.AttachmentImage:
		return doc.HasImage
	default:
		return false
	}
}
