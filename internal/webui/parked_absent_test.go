package webui_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "F-86 check: confirm nothing in internal/webui renders parked messages and that no message_failure row ever becomes an item in any status. Add an assertion."
//
// This is a negative requirement, and negative requirements rot silently:
// someone adds a "show me everything that needs attention" panel a year from
// now and nothing objects. The reasoning it protects: a parked message has
// no extractable document — extraction is precisely what failed — so it does
// not belong in a queue whose only purpose is deciding on documents. Its
// entire visibility surface is the park notification plus `postbode status`.
func TestParkedMessagesNeverAppearInTheReviewQueue(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	const parkedID = "msg-parked-not-reviewable"
	if _, ok, err := db.RecordMessageFailure(ctx, parkedID, "extract: malformed MIME", 1,
		time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC), time.Hour); err != nil || !ok {
		t.Fatalf("park: parked=%v err=%v", ok, err)
	}

	// One genuinely reviewable item, so the page is not trivially empty and
	// the assertion below is actually discriminating.
	stageTestItem(t, db, ctx, "msg-real", "billing@vendor.example", "Invoice", "", "")

	body := getList(t, ts.URL)

	if !strings.Contains(body, "2026-08-04-vendor-invoice.pdf") {
		t.Fatal("the reviewable item is missing; this test would pass vacuously")
	}
	if strings.Contains(body, parkedID) {
		t.Errorf("the review queue renders parked message %s; F-86 keeps it out — it has no document to review", parkedID)
	}
	if strings.Contains(strings.ToLower(body), "parked") {
		t.Errorf("the review queue mentions parking; that state belongs to postbode status, not here:\n%s", body)
	}

	// And it must never have become an item in any status.
	items, err := db.ItemsByMessageID(ctx, parkedID)
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("parked message produced %d queue item(s), want 0", len(items))
	}
}
