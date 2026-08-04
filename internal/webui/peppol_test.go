package webui_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
)

// stagePeppolSuppressedItem records a message and stages one item that L4's
// known-Peppol match already flagged suppressed_peppol (F-36) — the state
// internal/extract's real pipeline produces via
// dedup.MatchesKnownPeppol + queue.NewItem.SuppressedPeppol, reproduced
// directly here since this package only exercises the UI layer.
func stagePeppolSuppressedItem(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID, from string) int64 {
	t.Helper()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: gmailMessageID,
		From:           from,
		Subject:        "invoice",
	}); err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   gmailMessageID,
		OrigFilename:     "invoice.pdf",
		ProposedFilename: "acerta-invoice.pdf",
		MimeType:         "application/pdf",
		SizeBytes:        1024,
		SHA256:           gmailMessageID + "-hash",
		VendorDomain:     "acerta.be",
		SuppressedPeppol: true,
	})
	if err != nil {
		t.Fatalf("StageItem(%s): %v", gmailMessageID, err)
	}
	if res.Status != queue.StatusSuppressedPeppol {
		t.Fatalf("StageItem(%s): status = %s, want suppressed_peppol", gmailMessageID, res.Status)
	}
	return res.ItemID
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "`vendors.known_peppol` glob list: matching documents stage `suppressed_peppol` and are not uploadable without an explicit UI override action (F-36, AC-14)"
func TestSuppressedPeppolItemIsVisibleWithoutApproveButNeverDropped(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	id := stagePeppolSuppressedItem(t, db, ctx, "msg-1", "facturatie@acerta.be")

	page := getBody(t, ts, "/?t="+testToken)

	// AC-14's central assertion: the item is visible in the review queue
	// (never silently dropped) but Approve is not offered.
	if !strings.Contains(page, "suppressed") {
		t.Errorf("list page missing suppressed-peppol badge:\n%s", page)
	}
	approveForm := `action="/items/` + itoa(id) + `/approve"`
	if strings.Contains(page, approveForm) {
		t.Errorf("suppressed_peppol item offers Approve directly, want it disabled until override:\n%s", page)
	}
	overrideForm := `action="/items/` + itoa(id) + `/override-peppol"`
	if !strings.Contains(page, overrideForm) {
		t.Errorf("list page missing the override control:\n%s", page)
	}

	// Approve is genuinely refused server-side too, not just hidden in the UI.
	resp := postForm(t, ts, "/items/"+itoa(id)+"/approve", url.Values{"t": {testToken}})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("POST approve on suppressed_peppol item = %d, want 409 (F-41 lifecycle graph has no suppressed_peppol -> approved edge)", resp.StatusCode)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "`vendors.known_peppol` glob list: matching documents stage `suppressed_peppol` and are not uploadable without an explicit UI override action (F-36, AC-14)"
func TestOverridePeppolSuppressionUnlocksApprove(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	id := stagePeppolSuppressedItem(t, db, ctx, "msg-1", "facturatie@acerta.be")

	resp := postForm(t, ts, "/items/"+itoa(id)+"/override-peppol", url.Values{"t": {testToken}})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("override-peppol status = %d, want 200 (redirect followed)", resp.StatusCode)
	}

	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusStaged {
		t.Fatalf("Status after override = %s, want staged", item.Status)
	}

	page := getBody(t, ts, "/?t="+testToken)
	approveForm := `action="/items/` + itoa(id) + `/approve"`
	if !strings.Contains(page, approveForm) {
		t.Errorf("Approve is still not offered after override:\n%s", page)
	}

	approveResp := postForm(t, ts, "/items/"+itoa(id)+"/approve", url.Values{"t": {testToken}})
	defer func() { _ = approveResp.Body.Close() }()
	if approveResp.StatusCode != http.StatusOK {
		t.Fatalf("Approve after override status = %d, want 200 (redirect followed)", approveResp.StatusCode)
	}
	approved, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if approved.Status != queue.StatusApproved {
		t.Errorf("Status after Approve = %s, want approved", approved.Status)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "**L4** pre-flagging: future items whose `vendor_domain` matches a vendor previously marked `already_in_portal` stage pre-flagged `probably_already_handled` **with the reason and the teaching date shown**, still staged, still human-decidable, never auto-rejected (F-35, AC-13)"
//
// Unlike TestProbablyAlreadyHandledBadgeShowsReasonAndTeachingDate (Phase
// 8, which shares an identity_key between the two items as an
// incidental side effect of its fixture), this test uses two items with
// DIFFERENT identity keys — the realistic case, since two different
// invoices from the same vendor essentially never share one — to prove
// the badge detail is genuinely resolved by vendor_domain, not by a
// coincidentally-shared identity_key.
func TestProbablyAlreadyHandledBadgeResolvesByVendorDomainNotIdentityKey(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	firstID := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "first invoice", "", "identity-key-A")
	resp := postForm(t, ts, "/items/"+itoa(firstID)+"/already-in-portal", url.Values{"t": {testToken}, "note": {"arrived via Peppol"}})
	_ = resp.Body.Close()

	if _, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-2", From: "billing@ovh.com", Subject: "second invoice"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   "msg-2",
		OrigFilename:     "second.pdf",
		ProposedFilename: "second.pdf",
		SHA256:           "second-hash",
		IdentityKey:      "identity-key-B", // deliberately different
		VendorDomain:     "ovh.com",
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}
	item, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !item.ProbablyAlreadyHandled {
		t.Fatal("second item is not flagged probably_already_handled — L4 vendor-domain match did not fire")
	}

	page := getBody(t, ts, "/?t="+testToken)
	if !strings.Contains(page, "probably already handled") {
		t.Errorf("list page missing probably-already-handled badge:\n%s", page)
	}
	if !strings.Contains(page, "arrived via Peppol") {
		t.Errorf("list page missing the taught note:\n%s", page)
	}
	if !strings.Contains(page, "ovh.com") {
		t.Errorf("list page missing the teaching vendor domain:\n%s", page)
	}
}
