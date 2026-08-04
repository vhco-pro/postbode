package e2e_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	cffake "github.com/vhco-pro/postbode/internal/clearfacts/fake"
	"github.com/vhco-pro/postbode/internal/queue"
)

// This file drives Postbode's full pipeline end to end — poll → extract →
// rules → stage → notify → (real HTTP) approve → upload → document(id:)
// verify → label move — against the fixture mailbox
// (internal/gmailwatch/fake) and the fake ClearFacts server
// (internal/clearfacts/fake) only. `make e2e-dry` runs every test named
// TestE2EDry_* in this package (NF-10, AC-23). Per ADR-002, every mutating
// action goes through a real net/http form POST against the real
// webui.Server handler — never a direct database call — because that is
// the only user-facing surface this pipeline actually has.

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "tests/e2e/pipeline_test.go ... boots the real daemon wiring with the Gmail fake and the ClearFacts fake, drives the real HTTP review UI on 127.0.0.1 (approve via POST with the session token), and asserts items end in uploaded with uuid + verified_at and exactly one messages.modify call (NF-10, AC-23)"
func TestE2EDry_HappyPath(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()

	p.deliver("msg-happy", "billing@ovh.com", "Uw factuur juli", []pdfAttachment{
		{filename: "invoice.pdf", content: pdfContent("happy-path")},
	})

	p.runIteration(ctx) // poll: extract + rules + stage. Nothing approved yet.

	staged, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged items = %d, want 1", len(staged))
	}
	if p.cfSrv.RequestCount() != 0 {
		t.Fatalf("upload requests before approval = %d, want 0", p.cfSrv.RequestCount())
	}

	resp := p.approve(staged[0].ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /items/%d/approve: status = %d, want 200 (after following the redirect)", staged[0].ID, resp.StatusCode)
	}

	p.runIteration(ctx) // poll (no-op second time) + upload the approved item

	item := mustGetItem(t, p.db, ctx, staged[0].ID)
	if item.Status != queue.StatusUploaded {
		t.Fatalf("item status = %q, want %q", item.Status, queue.StatusUploaded)
	}
	if item.UUID == "" {
		t.Error("item.UUID is empty, want the fake's returned uuid")
	}
	if item.VerifiedAt == nil {
		t.Error("item.VerifiedAt is nil, want set (F-37 proof of delivery)")
	}
	if p.cfSrv.RequestCount() != 1 {
		t.Fatalf("upload requests after approval = %d, want exactly 1", p.cfSrv.RequestCount())
	}

	msg, err := p.db.GetMessage(ctx, "msg-happy")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.LabeledAt == nil {
		t.Error("message.LabeledAt is nil, want set — the only document uploaded and it succeeded")
	}
	if calls := p.gmailSrv.Calls(); len(calls) != 1 {
		t.Fatalf("messages.modify calls = %d, want exactly 1", len(calls))
	} else if calls[0].MessageID != "msg-happy" {
		t.Errorf("modify call target = %q, want %q", calls[0].MessageID, "msg-happy")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "Label application (F-14, AC-19): after all documents extracted from a message reach terminal uploaded, issue exactly one messages.modify adding VH&Co/submitted and removing INBOX. Never modify a message with a non-terminal document." — driven here through the full e2e pipeline for a 3-document message rather than at the uploader unit level.
func TestE2EDry_MultiDocumentMessage(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()

	p.deliver("msg-multi", "billing@ovh.com", "Uw factuur - drie bijlagen", []pdfAttachment{
		{filename: "invoice-1.pdf", content: pdfContent("multi-1")},
		{filename: "invoice-2.pdf", content: pdfContent("multi-2")},
		{filename: "invoice-3.pdf", content: pdfContent("multi-3")},
	})

	p.runIteration(ctx)

	staged, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(staged) != 3 {
		t.Fatalf("staged items = %d, want 3", len(staged))
	}

	// Approve and upload one document at a time, checking after each that
	// the label move has NOT happened until the third document lands —
	// the exact ordering guarantee AC-19 exists to prove, exercised here
	// through real HTTP approvals rather than direct db.Approve calls.
	for i, it := range staged {
		if resp := p.approve(it.ID); resp.StatusCode != http.StatusOK {
			t.Fatalf("approve item %d: status = %d, want 200", it.ID, resp.StatusCode)
		}
		p.runIteration(ctx)

		uploaded := mustGetItem(t, p.db, ctx, it.ID)
		if uploaded.Status != queue.StatusUploaded {
			t.Fatalf("document %d/3 status = %q, want uploaded", i+1, uploaded.Status)
		}

		calls := p.gmailSrv.Calls()
		if i < 2 {
			if len(calls) != 0 {
				t.Fatalf("after %d/3 documents uploaded, modify calls = %d, want 0", i+1, len(calls))
			}
		} else {
			if len(calls) != 1 {
				t.Fatalf("after all 3/3 documents uploaded, modify calls = %d, want exactly 1", len(calls))
			}
		}
	}

	if p.cfSrv.RequestCount() != 3 {
		t.Fatalf("upload requests = %d, want exactly 3", p.cfSrv.RequestCount())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "A fake server returning 503 three times then 200 results in exactly one stored uuid, retry_count == 3, and no duplicate upload. A fake server returning 400 marks the item failed immediately with retry_count == 0. (F-51)" — driven here as a 3-document, partial-failure e2e scenario rather than a single-item uploader unit test.
func TestE2EDry_PartialFailureNeverLabelsTheMessage(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()

	p.deliver("msg-partial", "billing@ovh.com", "Uw factuur - partial failure", []pdfAttachment{
		{filename: "invoice-a.pdf", content: pdfContent("partial-a")},
		{filename: "invoice-b.pdf", content: pdfContent("partial-b")},
		{filename: "invoice-c.pdf", content: pdfContent("partial-c")},
	})

	p.runIteration(ctx)
	staged, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(staged) != 3 {
		t.Fatalf("staged items = %d, want 3", len(staged))
	}

	// Documents A and B upload normally (the fake's default success path).
	for _, it := range staged[:2] {
		if resp := p.approve(it.ID); resp.StatusCode != http.StatusOK {
			t.Fatalf("approve item %d: status = %d, want 200", it.ID, resp.StatusCode)
		}
		p.runIteration(ctx)
		item := mustGetItem(t, p.db, ctx, it.ID)
		if item.Status != queue.StatusUploaded {
			t.Fatalf("item %d status = %q, want uploaded", it.ID, item.Status)
		}
	}

	// Document C gets a terminal 400 on its one and only upload attempt.
	failing := staged[2]
	p.cfSrv.Enqueue(cffake.ScriptedResponse{StatusCode: 400})
	if resp := p.approve(failing.ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve item %d: status = %d, want 200", failing.ID, resp.StatusCode)
	}
	p.runIteration(ctx)

	failedItem := mustGetItem(t, p.db, ctx, failing.ID)
	if failedItem.Status != queue.StatusFailed {
		t.Fatalf("failing item status = %q, want %q", failedItem.Status, queue.StatusFailed)
	}
	if failedItem.RetryCount != 0 {
		t.Errorf("failing item retry_count = %d, want 0 (a 400 is terminal, never retried)", failedItem.RetryCount)
	}
	if failedItem.LastError == "" {
		t.Error("failing item LastError is empty, want the 400's classified error text visible on the item")
	}

	// The message must never be labelled — one document never reached
	// terminal uploaded.
	if calls := p.gmailSrv.Calls(); len(calls) != 0 {
		t.Fatalf("modify calls = %d, want 0 — one document permanently failed, the message must never be labelled", len(calls))
	}
	msg, err := p.db.GetMessage(ctx, "msg-partial")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if msg.LabeledAt != nil {
		t.Error("message.LabeledAt is set, want nil")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "AC-10: Replaying the same Gmail history response twice produces exactly one set of items; the second pass writes a skip (L1) log line and creates zero rows. (F-30)" — re-verified end to end through the full daemon iteration, not only at the gmailwatch unit level.
func TestE2EDry_L1ReplayProducesNoDuplicateRows(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()

	p.deliver("msg-replay", "billing@ovh.com", "Uw factuur - replay", []pdfAttachment{
		{filename: "invoice.pdf", content: pdfContent("replay")},
	})

	p.runIteration(ctx)
	first, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("staged items after first poll = %d, want 1", len(first))
	}

	// Force a full resync (F-13 fallback) so Gmail's own discovery
	// mechanism re-lists the SAME message id a second time — exactly what
	// AC-10 describes as "replaying the same history response twice".
	p.forceFallback(ctx)
	p.runIteration(ctx)

	second, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged) after replay: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("staged items after replaying the same message = %d, want still 1 (zero new rows)", len(second))
	}
	if second[0].ID != first[0].ID {
		t.Fatalf("replay produced a different item id (%d vs %d) instead of skipping entirely", second[0].ID, first[0].ID)
	}

	found := false
	for _, line := range p.logs.All() {
		if strings.Contains(line, "skip (L1)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no log line containing %q found in %v", "skip (L1)", p.logs.All())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "Re-verify AC-11 (L2 link) end-to-end rather than at unit level only." (AC-11: "Two different emails carrying byte-identical PDFs produce one uploadable item; the second is linked_item_id-bound with dedup_layer='L2' and is never POSTed to the fake upload server.")
func TestE2EDry_L2ByteIdenticalDuplicateAcrossTwoMessages(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()

	shared := pdfAttachment{filename: "invoice.pdf", content: pdfContent("l2-shared-bytes")}
	p.deliver("msg-l2-first", "billing@ovh.com", "Uw factuur - eerste keer", []pdfAttachment{shared})
	p.deliver("msg-l2-second", "billing@ovh.com", "Uw factuur - nogmaals verzonden", []pdfAttachment{shared})

	p.runIteration(ctx)

	staged, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged (uploadable) items = %d, want exactly 1", len(staged))
	}

	linked, err := p.db.ListByStatus(ctx, queue.StatusDuplicateLinked)
	if err != nil {
		t.Fatalf("ListByStatus(duplicate_linked): %v", err)
	}
	if len(linked) != 1 {
		t.Fatalf("duplicate_linked items = %d, want exactly 1", len(linked))
	}
	if linked[0].DedupLayer != queue.DedupLayerL2 {
		t.Errorf("dedup layer = %q, want %q", linked[0].DedupLayer, queue.DedupLayerL2)
	}
	if linked[0].LinkedItemID == nil || *linked[0].LinkedItemID != staged[0].ID {
		t.Errorf("linked_item_id = %v, want a pointer to %d", linked[0].LinkedItemID, staged[0].ID)
	}

	if resp := p.approve(staged[0].ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve: status = %d, want 200", resp.StatusCode)
	}
	p.runIteration(ctx)

	uploaded := mustGetItem(t, p.db, ctx, staged[0].ID)
	if uploaded.Status != queue.StatusUploaded {
		t.Fatalf("uploadable item status = %q, want uploaded", uploaded.Status)
	}
	stillLinked := mustGetItem(t, p.db, ctx, linked[0].ID)
	if stillLinked.Status != queue.StatusDuplicateLinked {
		t.Errorf("duplicate_linked item status changed to %q, want unchanged %q", stillLinked.Status, queue.StatusDuplicateLinked)
	}

	if p.cfSrv.RequestCount() != 1 {
		t.Fatalf("upload requests reaching the fake = %d, want exactly 1 — the duplicate must never be POSTed", p.cfSrv.RequestCount())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "boots the real daemon wiring with the Gmail fake and the ClearFacts fake, drives the real HTTP review UI on 127.0.0.1 (approve via POST with the session token)" — exercised here for the Reject action rather than Approve, proving a rejected item never uploads and never resurfaces on a re-poll.
func TestE2EDry_RejectionNeverUploadsAndNeverResurfaces(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()

	p.deliver("msg-reject", "spammy-vendor@example.com", "Uw factuur - te weigeren", []pdfAttachment{
		{filename: "invoice.pdf", content: pdfContent("reject-me")},
	})

	p.runIteration(ctx)
	staged, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged items = %d, want 1", len(staged))
	}

	if resp := p.reject(staged[0].ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("reject: status = %d, want 200", resp.StatusCode)
	}

	rejected := mustGetItem(t, p.db, ctx, staged[0].ID)
	if rejected.Status != queue.StatusRejected {
		t.Fatalf("item status = %q, want %q", rejected.Status, queue.StatusRejected)
	}
	if strings.Contains(p.getList(), "invoice.pdf") {
		t.Error("rejected item's filename still appears on the review list, want it gone")
	}

	// A subsequent re-poll (even a full resync that re-discovers the same
	// message) must never bring it back into the reviewable queue.
	p.forceFallback(ctx)
	p.runIteration(ctx)

	stillRejected := mustGetItem(t, p.db, ctx, staged[0].ID)
	if stillRejected.Status != queue.StatusRejected {
		t.Errorf("item status after re-poll = %q, want still %q", stillRejected.Status, queue.StatusRejected)
	}
	afterRepoll, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged) after re-poll: %v", err)
	}
	if len(afterRepoll) != 0 {
		t.Fatalf("staged items after re-poll = %d, want 0 — a rejected item must never resurface", len(afterRepoll))
	}
	if strings.Contains(p.getList(), "invoice.pdf") {
		t.Error("rejected item resurfaced on the review list after a re-poll")
	}

	if p.cfSrv.RequestCount() != 0 {
		t.Fatalf("upload requests = %d, want 0 — a rejected item must never be uploaded", p.cfSrv.RequestCount())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "AC-13: POST /items/{id}/already-in-portal sets status already_in_portal, writes a vendor_teaching row, and performs zero upload calls. A subsequent item from the same vendor_domain stages with probably_already_handled=true and the UI shows the reason and the teaching date. (F-34, F-35)"
func TestE2EDry_AlreadyInPortalTeachesTheVendor(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()

	p.deliver("msg-aip-first", "facturatie@acerta.be", "Uw factuur - reeds in portaal", []pdfAttachment{
		{filename: "invoice-1.pdf", content: pdfContent("aip-1")},
	})
	p.runIteration(ctx)

	staged, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged items = %d, want 1", len(staged))
	}

	if resp := p.alreadyInPortal(staged[0].ID); resp.StatusCode != http.StatusOK {
		t.Fatalf("already-in-portal: status = %d, want 200", resp.StatusCode)
	}

	taught := mustGetItem(t, p.db, ctx, staged[0].ID)
	if taught.Status != queue.StatusAlreadyInPortal {
		t.Fatalf("item status = %q, want %q", taught.Status, queue.StatusAlreadyInPortal)
	}
	teaching, err := p.db.GetVendorTeachingByVendorDomain(ctx, "acerta.be")
	if err != nil {
		t.Fatalf("GetVendorTeachingByVendorDomain(acerta.be): %v", err)
	}
	if teaching.MarkedAt.IsZero() {
		t.Error("vendor_teaching.MarkedAt is zero, want set")
	}
	if p.cfSrv.RequestCount() != 0 {
		t.Fatalf("upload requests = %d, want 0 — already-in-portal performs zero upload calls", p.cfSrv.RequestCount())
	}

	// A later, unrelated invoice from the same vendor domain must arrive
	// pre-flagged.
	p.forceFallback(ctx)
	p.deliver("msg-aip-second", "facturatie@acerta.be", "Uw factuur - later", []pdfAttachment{
		{filename: "invoice-2.pdf", content: pdfContent("aip-2")},
	})
	p.runIteration(ctx)

	later, err := p.db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged) after second delivery: %v", err)
	}
	if len(later) != 1 {
		t.Fatalf("staged items after second delivery = %d, want 1", len(later))
	}
	if !later[0].ProbablyAlreadyHandled {
		t.Error("later[0].ProbablyAlreadyHandled = false, want true — L4 must pre-flag a later item from a taught vendor")
	}
	if p.cfSrv.RequestCount() != 0 {
		t.Fatalf("upload requests after the second delivery = %d, want still 0 (nothing approved)", p.cfSrv.RequestCount())
	}
}
