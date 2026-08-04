package rules_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/config"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/rules"
)

// fakeRecorder is an in-memory queue.DecisionLogEntry sink standing in for a
// queue.DB-backed Recorder, so this package's tests never touch SQLite.
type fakeRecorder struct {
	entries []queue.DecisionLogEntry
}

func (f *fakeRecorder) RecordDecision(_ context.Context, entry queue.DecisionLogEntry) error {
	f.entries = append(f.entries, entry)
	return nil
}

func mustLoad(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load(%s) error = %v", path, err)
	}
	return cfg
}

func intPtr(i int) *int { return &i }

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Every decision (`queued`, `denied`, `no-match-dropped`) written to `decision_log` with message id, matched rule index and reason (F-28, AC-8)"
func TestEngine_PRDSample_AC8(t *testing.T) {
	t.Parallel()

	cfg := mustLoad(t, "../config/testdata/prd_sample.yaml")
	engine := rules.New(cfg)
	rec := &fakeRecorder{}
	ctx := context.Background()

	tests := []struct {
		name             string
		doc              rules.Document
		wantDecision     rules.Decision
		wantRuleIndex    *int
		wantLowConfident bool
	}{
		{
			name: "newsletter with pdf is denied (rule index 2)",
			doc: rules.Document{
				GmailMessageID: "msg-newsletter",
				From:           "news@newsletter.example.com",
				Subject:        "This week's picks",
				HasPDF:         true,
			},
			wantDecision:  rules.DecisionDenied,
			wantRuleIndex: intPtr(2),
		},
		{
			name: "ovh with pdf is queued (rule index 0)",
			doc: rules.Document{
				GmailMessageID: "msg-ovh",
				From:           "billing@ovh.com",
				Subject:        "Your invoice",
				HasPDF:         true,
			},
			wantDecision:  rules.DecisionQueued,
			wantRuleIndex: intPtr(0),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := engine.EvaluateAndRecord(ctx, tt.doc, rec)
			if err != nil {
				t.Fatalf("EvaluateAndRecord() error = %v", err)
			}
			if res.Decision != tt.wantDecision {
				t.Errorf("Decision = %q, want %q", res.Decision, tt.wantDecision)
			}
			if res.MatchedRuleIndex == nil || *res.MatchedRuleIndex != *tt.wantRuleIndex {
				t.Errorf("MatchedRuleIndex = %v, want %v", res.MatchedRuleIndex, *tt.wantRuleIndex)
			}
			if res.LowConfidence != tt.wantLowConfident {
				t.Errorf("LowConfidence = %v, want %v", res.LowConfidence, tt.wantLowConfident)
			}
		})
	}

	if len(rec.entries) != 2 {
		t.Fatalf("decision_log has %d entries, want 2", len(rec.entries))
	}
	newsletter, ovh := rec.entries[0], rec.entries[1]
	if newsletter.GmailMessageID != "msg-newsletter" || newsletter.Decision != string(rules.DecisionDenied) ||
		newsletter.MatchedRuleIndex == nil || *newsletter.MatchedRuleIndex != 2 || newsletter.Reason == "" {
		t.Errorf("decision_log[0] = %+v, want denied/2 with a reason for msg-newsletter", newsletter)
	}
	if ovh.GmailMessageID != "msg-ovh" || ovh.Decision != string(rules.DecisionQueued) ||
		ovh.MatchedRuleIndex == nil || *ovh.MatchedRuleIndex != 0 || ovh.Reason == "" {
		t.Errorf("decision_log[1] = %+v, want queued/0 with a reason for msg-ovh", ovh)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Default when nothing matches: `queue_if(pdf_attachment || image_attachment || invoice_keywords)` with keywords at least `factuur, invoice, receipt, creditnota, rekening` over subject and body; recall over precision (F-27, AC-9)"
func TestEngine_DefaultHeuristic_AC9(t *testing.T) {
	t.Parallel()

	cfg := mustLoad(t, "../config/testdata/prd_sample.yaml") // has rules, none of which match these docs
	engine := rules.New(cfg)

	tests := []struct {
		name          string
		doc           rules.Document
		wantDecision  rules.Decision
		wantLowConfid bool
	}{
		{
			name: "AC-9: pdf attachment, factuur subject, no rule matches",
			doc: rules.Document{
				GmailMessageID: "msg-1",
				From:           "someone@example.be",
				Subject:        "Uw factuur juli",
				HasPDF:         true,
			},
			wantDecision:  rules.DecisionQueued,
			wantLowConfid: true,
		},
		{
			name: "image attachment alone queues",
			doc: rules.Document{
				GmailMessageID: "msg-2",
				From:           "someone@example.be",
				Subject:        "photo",
				HasImage:       true,
			},
			wantDecision:  rules.DecisionQueued,
			wantLowConfid: true,
		},
		{
			name: "keyword in body alone queues (keyword-only match)",
			doc: rules.Document{
				GmailMessageID: "msg-3",
				From:           "someone@example.be",
				Subject:        "hello",
				Body:           "please find your creditnota attached",
			},
			wantDecision:  rules.DecisionQueued,
			wantLowConfid: true,
		},
		{
			name: "keyword matches case-insensitively",
			doc: rules.Document{
				GmailMessageID: "msg-4",
				Subject:        "INVOICE for June",
			},
			wantDecision:  rules.DecisionQueued,
			wantLowConfid: true,
		},
		{
			name: "no attachment, no keyword, no rule: dropped",
			doc: rules.Document{
				GmailMessageID: "msg-5",
				From:           "someone@example.be",
				Subject:        "let's catch up",
				Body:           "coffee next week?",
			},
			wantDecision:  rules.DecisionDropped,
			wantLowConfid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := engine.Evaluate(tt.doc)
			if res.Decision != tt.wantDecision {
				t.Errorf("Decision = %q, want %q", res.Decision, tt.wantDecision)
			}
			if res.LowConfidence != tt.wantLowConfid {
				t.Errorf("LowConfidence = %v, want %v", res.LowConfidence, tt.wantLowConfid)
			}
			if res.MatchedRuleIndex != nil {
				t.Errorf("MatchedRuleIndex = %v, want nil (default path)", *res.MatchedRuleIndex)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Keyword-only or no-rule matches set `low_confidence=true` on the item (F-47, AC-9)"
func TestEngine_ExplicitRuleMatchIsNotLowConfidence(t *testing.T) {
	t.Parallel()

	cfg := mustLoad(t, "../config/testdata/prd_sample.yaml")
	engine := rules.New(cfg)

	res := engine.Evaluate(rules.Document{
		GmailMessageID: "msg-ovh",
		From:           "billing@ovh.com",
		HasPDF:         true,
	})
	if res.LowConfidence {
		t.Errorf("LowConfidence = true for an explicit allow-rule match, want false")
	}
	if res.MatchedRuleIndex == nil || *res.MatchedRuleIndex != 0 {
		t.Errorf("MatchedRuleIndex = %v, want 0", res.MatchedRuleIndex)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Per-document, first-match-wins evaluation with `allow` (→ queue) and `deny` (→ never queue) forms, matching on `from` (glob), `subject` (substring, case-insensitive), `has`/`has_no` (`pdf`, `image`), `list_unsubscribe` (bool) (F-26, AC-8)"
func TestEngine_FirstMatchWins(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		Rules: []config.Rule{
			{Deny: &config.Matcher{From: "*@vendor.example"}},
			{Allow: &config.Matcher{From: "*@vendor.example"}}, // shadowed: rule 0 must win
		},
	}
	engine := rules.New(cfg)

	res := engine.Evaluate(rules.Document{
		GmailMessageID: "msg-1",
		From:           "billing@vendor.example",
		HasPDF:         true,
	})
	if res.Decision != rules.DecisionDenied {
		t.Errorf("Decision = %q, want denied (first rule must win over the shadowed second rule)", res.Decision)
	}
	if res.MatchedRuleIndex == nil || *res.MatchedRuleIndex != 0 {
		t.Errorf("MatchedRuleIndex = %v, want 0", res.MatchedRuleIndex)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 6, Criterion: "Per-document, first-match-wins evaluation with `allow` (→ queue) and `deny` (→ never queue) forms, matching on `from` (glob), `subject` (substring, case-insensitive), `has`/`has_no` (`pdf`, `image`), `list_unsubscribe` (bool) (F-26, AC-8)"
func TestEngine_MatcherDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		matcher      config.Matcher
		doc          rules.Document
		wantDecision rules.Decision // decision when this is the only rule, as an allow rule
	}{
		{
			name:         "subject substring match is case-insensitive",
			matcher:      config.Matcher{Subject: "RECEIPT"},
			doc:          rules.Document{Subject: "Your receipt from acme"},
			wantDecision: rules.DecisionQueued,
		},
		{
			name:         "subject substring non-match",
			matcher:      config.Matcher{Subject: "receipt"},
			doc:          rules.Document{Subject: "unrelated"},
			wantDecision: rules.DecisionDropped,
		},
		{
			name:         "has_no pdf matches a doc without a pdf",
			matcher:      config.Matcher{HasNo: config.AttachmentPDF},
			doc:          rules.Document{HasImage: true},
			wantDecision: rules.DecisionQueued,
		},
		{
			name:         "has_no pdf does not match a doc with a pdf",
			matcher:      config.Matcher{HasNo: config.AttachmentPDF},
			doc:          rules.Document{HasPDF: true},
			wantDecision: rules.DecisionQueued, // falls through to default heuristic (pdf present)
		},
		{
			name:         "list_unsubscribe true matches",
			matcher:      config.Matcher{ListUnsubscribe: boolPtr(true)},
			doc:          rules.Document{ListUnsubscribe: true},
			wantDecision: rules.DecisionQueued,
		},
		{
			name:         "list_unsubscribe false does not match a true doc",
			matcher:      config.Matcher{ListUnsubscribe: boolPtr(false)},
			doc:          rules.Document{ListUnsubscribe: true},
			wantDecision: rules.DecisionDropped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Rules: []config.Rule{{Allow: &tt.matcher}}}
			engine := rules.New(cfg)
			res := engine.Evaluate(tt.doc)
			if res.Decision != tt.wantDecision {
				t.Errorf("Decision = %q, want %q", res.Decision, tt.wantDecision)
			}
		})
	}
}

func boolPtr(b bool) *bool { return &b }
