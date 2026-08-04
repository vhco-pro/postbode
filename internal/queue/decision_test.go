package queue_test

import (
	"context"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "internal/rules defines a Recorder interface expecting (*queue.DB).RecordDecision(ctx, queue.DecisionLogEntry) error, but that method does not exist yet on queue.DB. Add it — a small, additive method on the existing queue package, following its transactional conventions."
func TestRecordDecisionPersistsAndSatisfiesRulesRecorder(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	idx := 2
	entry := queue.DecisionLogEntry{
		GmailMessageID:   "msg-decision-1",
		Decision:         "denied",
		MatchedRuleIndex: &idx,
		Reason:           "matched deny rule",
	}
	if err := db.RecordDecision(ctx, entry); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	got, err := db.DecisionsByMessageID(ctx, "msg-decision-1")
	if err != nil {
		t.Fatalf("DecisionsByMessageID: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(DecisionsByMessageID) = %d, want 1", len(got))
	}
	if got[0].Decision != "denied" {
		t.Errorf("Decision = %q, want %q", got[0].Decision, "denied")
	}
	if got[0].MatchedRuleIndex == nil || *got[0].MatchedRuleIndex != 2 {
		t.Errorf("MatchedRuleIndex = %v, want 2", got[0].MatchedRuleIndex)
	}
	if got[0].Reason != "matched deny rule" {
		t.Errorf("Reason = %q, want %q", got[0].Reason, "matched deny rule")
	}
	if got[0].At.IsZero() {
		t.Error("At = zero time, want a recorded timestamp")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "internal/rules defines a Recorder interface expecting (*queue.DB).RecordDecision(ctx, queue.DecisionLogEntry) error"
func TestRecordDecisionWithoutMatchedRuleIndex(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	entry := queue.DecisionLogEntry{
		GmailMessageID: "msg-decision-2",
		Decision:       "no-match-dropped",
		Reason:         "default: no attachment and no invoice keyword",
	}
	if err := db.RecordDecision(ctx, entry); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}

	got, err := db.DecisionsByMessageID(ctx, "msg-decision-2")
	if err != nil {
		t.Fatalf("DecisionsByMessageID: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(DecisionsByMessageID) = %d, want 1", len(got))
	}
	if got[0].MatchedRuleIndex != nil {
		t.Errorf("MatchedRuleIndex = %v, want nil", got[0].MatchedRuleIndex)
	}
}
