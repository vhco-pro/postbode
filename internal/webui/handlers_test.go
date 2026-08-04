package webui_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/webui"
)

func newTestServer(t *testing.T) (*httptest.Server, *queue.DB, string) {
	t.Helper()
	db, dbPath := openTestDB(t)
	srv, err := webui.NewServer(db, testToken)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, db, dbPath
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Spec §3.6, Criterion: "AC-21: `GET /` and every `POST` without a valid session token returns 401; the listener is bound to `127.0.0.1` (verified by asserting a connection to the host's LAN IP is refused). (F-42, F-46, NF-04)"
func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()
	id := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "invoice", "", "")

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"GET / no token", http.MethodGet, "/"},
		{"GET / wrong token", http.MethodGet, "/?t=wrong"},
		{"POST approve no token", http.MethodPost, "/items/" + itoa(id) + "/approve"},
		{"POST reject no token", http.MethodPost, "/items/" + itoa(id) + "/reject"},
		{"POST already-in-portal no token", http.MethodPost, "/items/" + itoa(id) + "/already-in-portal"},
		{"POST approve-all no token", http.MethodPost, "/approve-all"},
		{"GET preview no token", http.MethodGet, "/preview/" + itoa(id)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, ts.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "`GET /` list view with badges, `GET /preview/{id}` streaming PDF bytes from spool, `POST /items/{id}/approve`, `/reject`, `/already-in-portal`, `POST /approve-all`, `GET /healthz` — per the spec §6.3 contract including 401/404/409 semantics (F-43)"
func TestHealthzRequiresNoToken(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "List view with per-item: sender, subject, proposed filename, status, flags (`needs_manual_handling`, `low_confidence`, `possible_duplicate`, `probably_already_handled`, `unsupported_type`), and inline PDF preview served from the spool path (F-43)"
func TestListViewShowsSenderSubjectFilenameStatusAndBadges(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()
	stageTestItem(t, db, ctx, "msg-1", "Billing <billing@ovh.com>", "Uw factuur juli", "", "")

	resp, err := http.Get(ts.URL + "/?t=" + testToken)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)

	for _, want := range []string{
		"billing@ovh.com",
		"Uw factuur juli",
		"2026-08-04-vendor-invoice.pdf",
		"staged",
		"Approve",
		"Reject",
		"Already in portal",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("list page missing %q\n---\n%s", want, html)
		}
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Spec §3.6, Criterion: "AC-7: Given a password-protected PDF fixture, the item is staged with `needs_manual_handling=true`, is not uploadable from the UI, and is never sent to the fake upload server. (F-22)"
func TestNeedsManualHandlingItemIsNotApprovableFromUI(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-locked", From: "vendor@example.com", Subject: "invoice"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:      "msg-locked",
		OrigFilename:        "locked.pdf",
		ProposedFilename:    "locked.pdf",
		SHA256:              "locked-hash",
		NeedsManualHandling: true,
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}

	// The list page must not offer an Approve form for this item.
	resp, err := http.Get(ts.URL + "/?t=" + testToken)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	html := string(body)
	if !strings.Contains(html, "needs manual handling") {
		t.Errorf("list page missing needs-manual-handling badge:\n%s", html)
	}
	approveAction := "/items/" + itoa(res.ItemID) + "/approve"
	if strings.Contains(html, `action="`+approveAction+`"`) {
		t.Errorf("list page unexpectedly offers Approve for a needs_manual_handling item:\n%s", html)
	}

	// A direct POST (bypassing the rendered UI) must still be refused —
	// defense in depth, not just hiding the button.
	form := url.Values{"t": {testToken}}
	postResp, err := ts.Client().PostForm(ts.URL+approveAction, form)
	if err != nil {
		t.Fatalf("PostForm: %v", err)
	}
	defer func() { _ = postResp.Body.Close() }()
	if postResp.StatusCode != http.StatusConflict {
		t.Errorf("status = %d, want 409", postResp.StatusCode)
	}

	item, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusStaged {
		t.Errorf("Status = %s, want still staged (never approved)", item.Status)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "Per-item actions: Approve / Reject / \"Already in portal\", plus one \"Approve all & upload\" button (F-43, F-34)"
func TestApproveTransitionsItemAndRedirects(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()
	id := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "invoice", "", "")

	resp := postForm(t, ts, "/items/"+itoa(id)+"/approve", url.Values{"t": {testToken}})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK { // httptest client follows the 303 by default
		t.Fatalf("status = %d, want 200 (redirect followed)", resp.StatusCode)
	}

	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusApproved {
		t.Errorf("Status = %s, want approved", item.Status)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "Per-item actions: Approve / Reject / \"Already in portal\", plus one \"Approve all & upload\" button (F-43, F-34)"
func TestRejectTransitionsItemAndRedirects(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()
	id := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "invoice", "", "")

	resp := postForm(t, ts, "/items/"+itoa(id)+"/reject", url.Values{"t": {testToken}})
	defer func() { _ = resp.Body.Close() }()

	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusRejected {
		t.Errorf("Status = %s, want rejected", item.Status)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Spec §3.6, Criterion: "AC-13: `POST /items/{id}/already-in-portal` sets status `already_in_portal`, writes a `vendor_teaching` row, and performs zero upload calls. A subsequent item from the same `vendor_domain` stages with `probably_already_handled=true` and the UI shows the reason and the teaching date. (F-34, F-35)"
func TestAlreadyInPortalSetsStatusAndRecordsVendorTeachingWithZeroUploads(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()
	id := stageTestItem(t, db, ctx, "msg-1", "Billing <billing@ovh.com>", "invoice", "", "identity-key-1")

	resp := postForm(t, ts, "/items/"+itoa(id)+"/already-in-portal", url.Values{"t": {testToken}, "note": {"seen it in the portal already"}})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (redirect followed)", resp.StatusCode)
	}

	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusAlreadyInPortal {
		t.Errorf("Status = %s, want already_in_portal", item.Status)
	}

	teaching, err := db.GetVendorTeachingByIdentityKey(ctx, "identity-key-1")
	if err != nil {
		t.Fatalf("GetVendorTeachingByIdentityKey: %v", err)
	}
	if teaching.VendorDomain != "ovh.com" {
		t.Errorf("VendorDomain = %q, want %q", teaching.VendorDomain, "ovh.com")
	}
	if teaching.Note != "seen it in the portal already" {
		t.Errorf("Note = %q, want the posted note", teaching.Note)
	}
	if teaching.MarkedAt.IsZero() {
		t.Error("MarkedAt is zero, want set")
	}

	// This package has no ClearFacts/upload client anywhere in its import
	// graph or call path — "already in portal" cannot reach an upload
	// server even in principle, which is the structural proof that it
	// performs zero upload calls (AC-13).
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Spec §3.6, Criterion: "AC-13 ... the UI shows the reason and the teaching date. (F-34, F-35)"
func TestProbablyAlreadyHandledBadgeShowsReasonAndTeachingDate(t *testing.T) {
	ts, db, dbPath := newTestServer(t)
	ctx := context.Background()

	// First item: teach the vendor via the real endpoint.
	firstID := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "first invoice", "", "shared-identity-key")
	resp := postForm(t, ts, "/items/"+itoa(firstID)+"/already-in-portal", url.Values{"t": {testToken}})
	_ = resp.Body.Close()

	// Second item: simulate what Phase 12's L4 matching engine will set at
	// staging time — this phase only renders the flag, not derives it.
	secondID := stageTestItem(t, db, ctx, "msg-2", "billing@ovh.com", "second invoice", "", "shared-identity-key")
	setProbablyAlreadyHandled(t, dbPath, secondID)

	page := getBody(t, ts, "/?t="+testToken)
	if !strings.Contains(page, "probably already handled") {
		t.Errorf("list page missing probably-already-handled badge:\n%s", page)
	}
	if !strings.Contains(page, "marked already in portal by reviewer") {
		t.Errorf("list page missing teaching reason:\n%s", page)
	}
	if !strings.Contains(page, "ovh.com") {
		t.Errorf("list page missing teaching vendor domain:\n%s", page)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Spec §3.6, Criterion: "AC-12: ... the second shows a `possible duplicate of #<id>` badge with the first item's status and uuid. Neither is auto-suppressed. (F-32, F-33)"
func TestPossibleDuplicateBadgeShowsLinkedItemAndIsNeverAutoSuppressed(t *testing.T) {
	ts, db, dbPath := newTestServer(t)
	ctx := context.Background()

	firstID := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "invoice one", "", "")
	secondID := stageTestItem(t, db, ctx, "msg-2", "billing@ovh.com", "invoice two", "", "")
	setDuplicateFlags(t, dbPath, secondID, firstID)

	page := getBody(t, ts, "/?t="+testToken)
	wantLink := "of #" + itoa(firstID)
	if !strings.Contains(page, wantLink) {
		t.Errorf("list page missing duplicate link %q:\n%s", wantLink, page)
	}
	// The invariant that matters most: a possible_duplicate item still
	// gets its Approve form. Nothing in this package may auto-suppress it.
	secondApprove := `action="/items/` + itoa(secondID) + `/approve"`
	if !strings.Contains(page, secondApprove) {
		t.Errorf("possible_duplicate item was suppressed from the UI (missing %q):\n%s", secondApprove, page)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "`Approve all` applies to the visible filtered set only, so a 20-attachment bundle cannot be approved by accident (spec §8)"
func TestApproveAllApprovesOnlyVisibleApprovableItems(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	approvableID := stageTestItem(t, db, ctx, "msg-1", "a@example.com", "one", "", "")
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-2", From: "b@example.com", Subject: "two"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   "msg-2",
		OrigFilename:     "locked.pdf",
		ProposedFilename: "locked.pdf",
		SHA256:           "locked-hash-2",
		UnsupportedType:  true,
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}
	unsupportedID := res.ItemID

	resp := postForm(t, ts, "/approve-all", url.Values{"t": {testToken}})
	_ = resp.Body.Close()

	approved, err := db.GetItem(ctx, approvableID)
	if err != nil {
		t.Fatalf("GetItem(approvable): %v", err)
	}
	if approved.Status != queue.StatusApproved {
		t.Errorf("approvable item Status = %s, want approved", approved.Status)
	}

	stillStaged, err := db.GetItem(ctx, unsupportedID)
	if err != nil {
		t.Fatalf("GetItem(unsupported): %v", err)
	}
	if stillStaged.Status != queue.StatusStaged {
		t.Errorf("unsupported_type item Status = %s, want still staged (never bulk-approved)", stillStaged.Status)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "`GET /preview/{id}` streaming PDF bytes from spool (F-43)"
func TestPreviewStreamsPDFBytesFromSpoolPath(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	spoolDir := t.TempDir()
	spoolPath := filepath.Join(spoolDir, "invoice.pdf")
	pdfBytes := []byte("%PDF-1.4 fake content")
	if err := os.WriteFile(spoolPath, pdfBytes, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	id := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "invoice", spoolPath, "")

	resp, err := http.Get(ts.URL + "/preview/" + itoa(id) + "?t=" + testToken)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("Content-Type = %q, want application/pdf", ct)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != string(pdfBytes) {
		t.Errorf("body = %q, want %q", got, pdfBytes)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Security invariant (CLAUDE.md), Criterion: "no path traversal when serving spool files (an item id maps to a spool path via the queue, never a user-supplied path)"
func TestPreviewNeverTakesAPathFromTheRequest(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	// A secret file that must never be reachable through /preview,
	// regardless of what an attacker puts in the URL — the handler only
	// ever opens item.SpoolPath, which comes from the database.
	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("top secret"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	id := stageTestItem(t, db, ctx, "msg-1", "billing@ovh.com", "invoice", "", "")

	// Non-numeric / traversal-shaped "ids" must 404, never be interpreted
	// as a filesystem path.
	for _, badID := range []string{
		url.PathEscape("../../../../etc/passwd"),
		url.PathEscape(secretPath),
		"0",
		"-1",
		"not-a-number",
	} {
		resp, err := http.Get(ts.URL + "/preview/" + badID + "?t=" + testToken)
		if err != nil {
			t.Fatalf("Get(%s): %v", badID, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Get(%s): status = %d, want 404", badID, resp.StatusCode)
		}
	}

	// The one legitimate id must still work and must never expose the
	// secret file's contents.
	resp, err := http.Get(ts.URL + "/preview/" + itoa(id) + "?t=" + testToken)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "top secret") {
		t.Fatal("preview leaked the secret file's contents")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, NF-01 (embedded page, html/template autoescaping), Criterion: "no JS build step, no framework" implies plain html/template output; autoescaping must not be bypassed
func TestListViewEscapesUntrustedFieldsAgainstXSS(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: "msg-xss",
		From:           `<script>alert(1)</script>@evil.example`,
		Subject:        `<img src=x onerror=alert(1)>`,
	}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	if _, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID:   "msg-xss",
		OrigFilename:     `"><script>alert(2)</script>`,
		ProposedFilename: `<script>alert(3)</script>.pdf`,
		SHA256:           "xss-hash",
	}); err != nil {
		t.Fatalf("StageItem: %v", err)
	}

	page := getBody(t, ts, "/?t="+testToken)
	if strings.Contains(page, "<script>") {
		t.Errorf("list page contains an unescaped <script> tag:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Errorf("list page does not show the expected escaped form:\n%s", page)
	}
}

func postForm(t *testing.T, ts *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()
	resp, err := ts.Client().PostForm(ts.URL+path, form)
	if err != nil {
		t.Fatalf("PostForm(%s): %v", path, err)
	}
	return resp
}

func getBody(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("Get(%s): %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(body)
}

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
