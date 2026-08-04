package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/vhco-pro/postbode/internal/clearfacts"
	cffake "github.com/vhco-pro/postbode/internal/clearfacts/fake"
	"github.com/vhco-pro/postbode/internal/config"
	"github.com/vhco-pro/postbode/internal/daemon"
	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/gmailwatch"
	gwfake "github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/rules"
	"github.com/vhco-pro/postbode/internal/uploader"
	"github.com/vhco-pro/postbode/internal/webui"
)

const (
	e2eCompanyNumber = "BE0999999999"
	e2eLabelID       = "Label_e2e_submitted"
	e2eSessionToken  = "e2e-fixed-session-token-0123456789"
)

// captureLog is a concurrency-safe []string sink used as a Logf across the
// pipeline, so a scenario can assert on the F-30 "skip (L1)" line (AC-10)
// without depending on a second logging subsystem.
type captureLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLog) Logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *captureLog) All() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.lines))
	copy(out, c.lines)
	return out
}

// pipeline bundles the full Postbode wiring over fakes only (NF-09): a
// fake Gmail mailbox (internal/gmailwatch/fake), a fake ClearFacts upload
// endpoint (internal/clearfacts/fake), a real temp SQLite queue, a real
// temp spool dir (internal/extract), and the real webui.Server handler
// served on a real loopback httptest.Server — assembled exactly the way
// cmd/postbode's daemon wiring does (mirrors internal/daemon/daemon_test.go's
// newTestDaemon, extended with the real HTTP review UI per ADR-002).
type pipeline struct {
	t *testing.T

	db       *queue.DB
	gmailSrv *gwfake.Server
	cfSrv    *cffake.Server
	watcher  *gmailwatch.Watcher
	uploader *uploader.Uploader
	daemon   *daemon.Daemon
	ui       *httptest.Server
	notifier *notify.Fake
	logs     *captureLog

	mu       sync.Mutex
	messages map[string][]byte // gmail message id -> raw RFC822 bytes
}

// newPipeline wires one full pipeline instance for one test. Every fake is
// closed via t.Cleanup; the returned pipeline's DB lives in a fresh
// t.TempDir(), never a shared path (NF-09 / test hygiene, mirrors every
// other phase's openTestDB helper).
func newPipeline(t *testing.T) *pipeline {
	t.Helper()
	t.Setenv("CF_TOKEN", "cf_e2e_test_pat_value")

	dbPath := t.TempDir() + "/queue.db"
	db, err := queue.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	gmailSrv := gwfake.NewServer()
	t.Cleanup(gmailSrv.Close)
	gmailSrv.Labels = []*gmail.Label{{Id: e2eLabelID, Name: gmailwatch.SubmittedLabelName}}

	p := &pipeline{
		t:        t,
		db:       db,
		gmailSrv: gmailSrv,
		messages: map[string][]byte{},
		notifier: &notify.Fake{},
		logs:     &captureLog{},
	}
	gmailSrv.MessagesListFunc = p.handleMessagesList
	gmailSrv.MessagesGetFunc = p.handleMessagesGet
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) { return &gmail.Profile{HistoryId: 1}, nil }

	cfSrv := cffake.New()
	t.Cleanup(cfSrv.Close)
	p.cfSrv = cfSrv

	svc, err := gmail.NewService(context.Background(),
		option.WithEndpoint(gmailSrv.URL),
		option.WithHTTPClient(http.DefaultClient),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("gmail.NewService: %v", err)
	}

	watcher := &gmailwatch.Watcher{
		Service:   svc,
		DB:        db,
		Extractor: extract.New(t.TempDir(), db),
		Rules:     rules.New(config.Default()),
		Notifier:  p.notifier,
		UserID:    "me",
		Config:    gmailwatch.Config{QueryWindowDays: 30},
		Logf:      p.logs.Logf,
	}
	p.watcher = watcher

	client := clearfacts.NewClient(clearfacts.EnvTokenSource{}, clearfacts.WithEndpoint(cfSrv.URL()))
	up := &uploader.Uploader{
		DB:            db,
		Client:        client,
		CompanyNumber: e2eCompanyNumber,
		LabelApplier:  watcher,
		Notifier:      p.notifier,
		Logf:          p.logs.Logf,
	}
	p.uploader = up

	p.daemon = &daemon.Daemon{
		DB:            db,
		Watcher:       watcher,
		Uploader:      up,
		Notifier:      p.notifier,
		PollInterval:  time.Hour, // irrelevant — this harness drives RunIteration explicitly
		StaleClaimAge: time.Hour,
		Logf:          p.logs.Logf,
	}

	srv, err := webui.NewServer(db, e2eSessionToken)
	if err != nil {
		t.Fatalf("webui.NewServer: %v", err)
	}
	// A real net/http listener on 127.0.0.1, driven with real form POSTs
	// (ADR-002) — httptest.NewServer binds an actual loopback TCP socket
	// and serves srv.Handler() over it, exactly what a browser would see,
	// without needing a browser.
	p.ui = httptest.NewServer(srv.Handler())
	t.Cleanup(p.ui.Close)

	return p
}

func (p *pipeline) handleMessagesList(q, pageToken string) (*gmail.ListMessagesResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var msgs []*gmail.Message
	for id := range p.messages {
		msgs = append(msgs, &gmail.Message{Id: id})
	}
	return &gmail.ListMessagesResponse{Messages: msgs}, nil
}

func (p *pipeline) handleMessagesGet(id, format string) (*gmail.Message, error) {
	p.mu.Lock()
	raw, ok := p.messages[id]
	p.mu.Unlock()
	if !ok {
		return nil, &gwfake.APIError{Code: http.StatusNotFound, Message: "no such message: " + id}
	}
	return &gmail.Message{
		Id:           id,
		Raw:          base64.URLEncoding.EncodeToString(raw),
		InternalDate: time.Now().UnixMilli(),
	}, nil
}

// deliver adds one message to the fixture mailbox, discoverable by the next
// fallback list poll. It returns the gmail message id assigned.
func (p *pipeline) deliver(id, from, subject string, pdfs []pdfAttachment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages[id] = buildRawMessage(p.t, from, subject, pdfs)
}

// forceFallback resets sync_state.history_id, forcing the next Poll to use
// the F-13 windowed fallback (a full mailbox resync) instead of F-12's
// incremental history sync — used by scenarios that need to prove a
// message does not resurface (rejection memory, L1) even when Gmail's
// discovery mechanism re-lists it. The submitted-label id, once resolved,
// is preserved.
func (p *pipeline) forceFallback(ctx context.Context) {
	p.t.Helper()
	st, err := p.db.GetSyncState(ctx)
	if err != nil {
		p.t.Fatalf("GetSyncState: %v", err)
	}
	st.HistoryID = ""
	if err := p.db.SaveSyncState(ctx, st); err != nil {
		p.t.Fatalf("SaveSyncState: %v", err)
	}
}

// runIteration is a thin wrapper naming what it does in each scenario:
// poll (extract + rules + stage) then, if the F-15 label is resolved,
// drain the approved queue through the uploader.
func (p *pipeline) runIteration(ctx context.Context) {
	p.daemon.RunIteration(ctx)
}

// --- real HTTP against the real review UI (ADR-002) -----------------------

func (p *pipeline) postForm(path string, form url.Values) *http.Response {
	p.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("t", e2eSessionToken)
	resp, err := http.PostForm(p.ui.URL+path, form)
	if err != nil {
		p.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func (p *pipeline) approve(itemID int64) *http.Response {
	p.t.Helper()
	resp := p.postForm(fmt.Sprintf("/items/%d/approve", itemID), nil)
	resp.Body.Close()
	return resp
}

func (p *pipeline) reject(itemID int64) *http.Response {
	p.t.Helper()
	resp := p.postForm(fmt.Sprintf("/items/%d/reject", itemID), nil)
	resp.Body.Close()
	return resp
}

func (p *pipeline) alreadyInPortal(itemID int64) *http.Response {
	p.t.Helper()
	resp := p.postForm(fmt.Sprintf("/items/%d/already-in-portal", itemID), nil)
	resp.Body.Close()
	return resp
}

// getList performs a real, authenticated GET / against the review UI and
// returns the raw HTML body — used where a scenario needs to assert on
// what the human reviewer would actually see (badges, disabled controls),
// not just on database state.
func (p *pipeline) getList() string {
	p.t.Helper()
	resp, err := http.Get(p.ui.URL + "/?t=" + url.QueryEscape(e2eSessionToken))
	if err != nil {
		p.t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		p.t.Fatalf("read GET / body: %v", err)
	}
	return buf.String()
}

// --- synthetic fixture construction ----------------------------------------

// pdfAttachment is one PDF part to embed in a synthetic raw RFC822 message.
type pdfAttachment struct {
	filename string
	content  []byte
}

// pdfContent returns a minimal, valid-enough-to-sniff PDF byte sequence
// (SniffMIME only requires the "%PDF-" magic prefix, extract/sniff.go)
// carrying marker so two calls with different markers never collide on
// SHA-256, and two calls with the SAME marker are byte-identical — exactly
// what the L2 scenario needs. Never contains "/Encrypt" (F-22's detector).
func pdfContent(marker string) []byte {
	return []byte(fmt.Sprintf("%%PDF-1.4\n%% e2e-fixture-marker:%s\n1 0 obj<< /Type /Catalog >>endobj\ntrailer<< /Root 1 0 R >>\n%%%%EOF", marker))
}

func randomID(t *testing.T, prefix string) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return prefix + "-" + hex.EncodeToString(b)
}

// buildRawMessage constructs a syntactically valid multipart/mixed RFC822
// message carrying len(pdfs) application/pdf attachments — the same shape
// testdata/nested-three-pdfs.eml uses, built parametrically so each
// scenario controls exactly how many documents and what bytes they carry.
func buildRawMessage(t *testing.T, from, subject string, pdfs []pdfAttachment) []byte {
	t.Helper()
	boundary := "E2EBOUNDARY" + randomID(t, "")

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: review@vhco.pro\r\n")
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: <%s@fixtures.postbode.e2e>\r\n", randomID(t, "msg"))
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundary)

	buf.WriteString("--" + boundary + "\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	buf.WriteString("See attached invoice(s).\r\n")

	for _, p := range pdfs {
		buf.WriteString("--" + boundary + "\r\n")
		fmt.Fprintf(&buf, "Content-Type: application/pdf; name=%q\r\n", p.filename)
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%q\r\n", p.filename)
		buf.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		writeBase64Wrapped(&buf, p.content)
	}

	buf.WriteString("--" + boundary + "--\r\n")
	return buf.Bytes()
}

// writeBase64Wrapped writes data as base64, wrapped at 76 chars per line
// (RFC 2045), the same convention testdata/*.eml's fixtures use.
func writeBase64Wrapped(buf *bytes.Buffer, data []byte) {
	enc := base64.StdEncoding.EncodeToString(data)
	for len(enc) > 76 {
		buf.WriteString(enc[:76])
		buf.WriteString("\r\n")
		enc = enc[76:]
	}
	buf.WriteString(enc)
	buf.WriteString("\r\n")
}

// --- assertion helpers ------------------------------------------------------

func mustGetItem(t *testing.T, db *queue.DB, ctx context.Context, id int64) *queue.Item {
	t.Helper()
	item, err := db.GetItem(ctx, id)
	if err != nil {
		t.Fatalf("GetItem(%d): %v", id, err)
	}
	return item
}
