package queue

import (
	"context"
	"fmt"
	"time"
)

// RecordDecision persists one decision_log row (F-28: every rules-engine
// decision — queued, denied, no-match-dropped — recorded with message id,
// matched rule index and reason). decision_log is queue's table (spec
// §5.2); this method is what makes *DB satisfy rules.Recorder without
// internal/rules ever touching SQL directly.
//
// This method did not exist before Phase 7: internal/rules.Recorder
// (Phase 6) already declared the interface *DB needed to satisfy, but the
// implementation was never added until gmailwatch (Phase 7) needed to wire
// rules.Engine.EvaluateAndRecord against a real queue.DB.
func (db *DB) RecordDecision(ctx context.Context, entry DecisionLogEntry) error {
	if entry.GmailMessageID == "" {
		return fmt.Errorf("queue: RecordDecision: gmail message id is required")
	}
	at := entry.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := db.sqlDB.ExecContext(ctx, `
		INSERT INTO decision_log (gmail_message_id, decision, matched_rule_index, reason, at)
		VALUES (?, ?, ?, ?, ?)
	`, entry.GmailMessageID, entry.Decision, entry.MatchedRuleIndex, stringOrNil(entry.Reason), timeToDB(at))
	if err != nil {
		return fmt.Errorf("queue: RecordDecision: %w", err)
	}
	return nil
}

// DecisionsByMessageID returns every decision_log row recorded for one
// gmail_message_id, oldest first. Test-support and future CLI (`postbode
// log`) surface.
func (db *DB) DecisionsByMessageID(ctx context.Context, gmailMessageID string) ([]DecisionLogEntry, error) {
	rows, err := db.sqlDB.QueryContext(ctx, `
		SELECT id, gmail_message_id, decision, matched_rule_index, reason, at
		FROM decision_log WHERE gmail_message_id = ? ORDER BY id
	`, gmailMessageID)
	if err != nil {
		return nil, fmt.Errorf("queue: DecisionsByMessageID(%s): %w", gmailMessageID, err)
	}
	defer rows.Close()

	var out []DecisionLogEntry
	for rows.Next() {
		var (
			e                DecisionLogEntry
			matchedRuleIndex *int
			reason           *string
			at               string
		)
		if err := rows.Scan(&e.ID, &e.GmailMessageID, &e.Decision, &matchedRuleIndex, &reason, &at); err != nil {
			return nil, fmt.Errorf("queue: DecisionsByMessageID(%s): scan: %w", gmailMessageID, err)
		}
		e.MatchedRuleIndex = matchedRuleIndex
		if reason != nil {
			e.Reason = *reason
		}
		t, err := time.Parse(timeLayout, at)
		if err != nil {
			return nil, fmt.Errorf("queue: DecisionsByMessageID(%s): parse at: %w", gmailMessageID, err)
		}
		e.At = t.UTC()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: DecisionsByMessageID(%s): %w", gmailMessageID, err)
	}
	return out, nil
}
