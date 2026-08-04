package daemon_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

const fixtureDir = "../../testdata"

const testCompanyNumber = "BE0123456789"

func openTestDB(t *testing.T) *queue.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := queue.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func rawFixtureMessage(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return base64.URLEncoding.EncodeToString(data)
}

func gmailServiceFor(t *testing.T, fk *gwfake.Server) *gmail.Service {
	t.Helper()
	svc, err := gmail.NewService(context.Background(),
		option.WithEndpoint(fk.URL),
		option.WithHTTPClient(http.DefaultClient),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("gmail.NewService: %v", err)
	}
	return svc
}

// newTestDaemon wires a Daemon exactly the way cmd/postbode's daemon
// wiring does, over fakes only (NF-09): a fake Gmail server, a real
// extract.Extractor + rules.Engine (config.Default()'s ruleset, so any
// PDF-carrying message queues by default), and a fake ClearFacts server.
func newTestDaemon(t *testing.T, gmailSrv *gwfake.Server, cfSrv *cffake.Server, notifier notify.Notifier, logf func(string, ...any)) (*daemon.Daemon, *queue.DB) {
	t.Helper()
	db := openTestDB(t)
	svc := gmailServiceFor(t, gmailSrv)

	w := &gmailwatch.Watcher{
		Service:   svc,
		DB:        db,
		Extractor: extract.New(t.TempDir(), db),
		Rules:     rules.New(config.Default()),
		Notifier:  notifier,
		UserID:    "me",
		Config:    gmailwatch.Config{QueryWindowDays: 30},
		Logf:      logf,
	}

	client := clearfacts.NewClient(clearfacts.EnvTokenSource{}, clearfacts.WithEndpoint(cfSrv.URL()))
	up := &uploader.Uploader{
		DB:            db,
		Client:        client,
		CompanyNumber: testCompanyNumber,
		LabelApplier:  w,
		Notifier:      notifier,
		Logf:          logf,
	}

	d := &daemon.Daemon{
		DB:            db,
		Watcher:       w,
		Uploader:      up,
		Notifier:      notifier,
		PollInterval:  time.Hour, // irrelevant for RunIteration-driven tests
		StaleClaimAge: time.Hour,
		Logf:          logf,
	}
	return d, db
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "the daemon loop must be testable: structure it so a test can run one poll+upload iteration with fakes and a cancellable context, without sleeping for 5 minutes."
func TestRunIterationPollsStagesAndLaterUploadsAfterApproval(t *testing.T) {
	t.Setenv("CF_TOKEN", "cf_test_pat_value")

	gmailSrv := gwfake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.Labels = []*gmail.Label{{Id: "Label_1", Name: gmailwatch.SubmittedLabelName}}
	raw := rawFixtureMessage(t, "rfc2047-filename.eml")
	gmailSrv.MessagesListFunc = func(q, pageToken string) (*gmail.ListMessagesResponse, error) {
		return &gmail.ListMessagesResponse{Messages: []*gmail.Message{{Id: "msg-daemon-1"}}}, nil
	}
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) {
		return &gmail.Profile{HistoryId: 100}, nil
	}
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{HistoryId: startHistoryID}, nil
	}

	cfSrv := cffake.New()
	defer cfSrv.Close()

	fakeNotifier := &notify.Fake{}
	d, db := newTestDaemon(t, gmailSrv, cfSrv, fakeNotifier, nil)
	ctx := context.Background()

	d.RunIteration(ctx)

	if !d.LabelResolved() {
		t.Fatal("LabelResolved() = false after a poll against a server carrying the label")
	}
	staged, err := db.ListByStatus(ctx, queue.StatusStaged)
	if err != nil {
		t.Fatalf("ListByStatus(staged): %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("staged items after first RunIteration = %d, want 1", len(staged))
	}
	if cfSrv.RequestCount() != 0 {
		t.Fatalf("ClearFacts upload requests before approval = %d, want 0", cfSrv.RequestCount())
	}

	if err := db.Approve(ctx, staged[0].ID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	d.RunIteration(ctx)

	if cfSrv.RequestCount() != 1 {
		t.Fatalf("ClearFacts upload requests after approval + second RunIteration = %d, want 1", cfSrv.RequestCount())
	}
	item, err := db.GetItem(ctx, staged[0].ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusUploaded {
		t.Errorf("item status = %q, want %q", item.Status, queue.StatusUploaded)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-15: refuse to start the uploader if genuinely absent, but keep polling/staging."
func TestRunIterationDisablesUploaderWhenLabelAbsentButKeepsPolling(t *testing.T) {
	gmailSrv := gwfake.NewServer()
	defer gmailSrv.Close()
	// No VH&Co/submitted label in this mailbox at all — an authenticated
	// list that came back without it, exactly F-15's ErrLabelNotFound
	// trigger.
	gmailSrv.Labels = []*gmail.Label{{Id: "Label_1", Name: "INBOX"}}
	raw := rawFixtureMessage(t, "no-attachments.eml")
	gmailSrv.MessagesListFunc = func(q, pageToken string) (*gmail.ListMessagesResponse, error) {
		return &gmail.ListMessagesResponse{Messages: []*gmail.Message{{Id: "msg-noattach"}}}, nil
	}
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) {
		return &gmail.Profile{HistoryId: 1}, nil
	}

	cfSrv := cffake.New()
	defer cfSrv.Close()

	fakeNotifier := &notify.Fake{}
	d, db := newTestDaemon(t, gmailSrv, cfSrv, fakeNotifier, nil)
	ctx := context.Background()

	// Pre-stage and approve an item directly, bypassing the poll/rules
	// path, so this test isolates "does RunBatch get called at all"
	// from "does this particular message ever get staged."
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-preexisting", From: "vendor@example.com", Subject: "invoice"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	spoolPath := filepath.Join(t.TempDir(), "preexisting.pdf")
	if err := os.WriteFile(spoolPath, []byte("%PDF-1.4 fake"), 0o600); err != nil {
		t.Fatalf("write spool file: %v", err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-preexisting",
		SpoolPath:      spoolPath,
		OrigFilename:   "invoice.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      13,
		SHA256:         "deadbeef",
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}
	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	d.RunIteration(ctx)

	if d.LabelResolved() {
		t.Fatal("LabelResolved() = true, want false when the label genuinely does not exist")
	}
	if cfSrv.RequestCount() != 0 {
		t.Fatalf("ClearFacts upload requests with label absent = %d, want 0 (uploader must not start)", cfSrv.RequestCount())
	}
	// Polling/staging must still have happened despite the uploader being
	// disabled — F-15 scopes the refusal to the uploader only.
	seen, err := db.MessageSeen(ctx, "msg-noattach")
	if err != nil {
		t.Fatalf("MessageSeen: %v", err)
	}
	if !seen {
		t.Error("message was not recorded — polling/staging must continue even when the label is absent")
	}
	found := false
	for _, m := range fakeNotifier.All() {
		if strings.Contains(m, "not found") {
			found = true
		}
	}
	if !found {
		t.Errorf("notifier messages = %v, want one mentioning the missing label", fakeNotifier.All())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "Graceful shutdown on SIGINT/SIGTERM: stop accepting work ... a half-written state must never result."
func TestRunReturnsCleanlyOnContextCancellation(t *testing.T) {
	gmailSrv := gwfake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.Labels = []*gmail.Label{{Id: "Label_1", Name: gmailwatch.SubmittedLabelName}}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) { return &gmail.Profile{HistoryId: 1}, nil }
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{HistoryId: startHistoryID}, nil
	}
	cfSrv := cffake.New()
	defer cfSrv.Close()

	d, _ := newTestDaemon(t, gmailSrv, cfSrv, &notify.Fake{}, nil)
	d.PollInterval = 5 * time.Millisecond
	d.PruneInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	time.Sleep(30 * time.Millisecond) // let a couple of ticks fire
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error %v, want nil on clean shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of context cancellation")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "Run spool pruning on a periodic tick (retention_days, default 30)."
func TestPruneOnceRemovesSpoolFileOfUploadedItem(t *testing.T) {
	gmailSrv := gwfake.NewServer()
	defer gmailSrv.Close()
	cfSrv := cffake.New()
	defer cfSrv.Close()

	d, db := newTestDaemon(t, gmailSrv, cfSrv, &notify.Fake{}, nil)
	d.RetentionDays = -1 // negative: bypasses the zero-defaults-to-30 guard, forces immediate pruning

	ctx := context.Background()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-prune", From: "v@example.com", Subject: "s"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	spoolPath := filepath.Join(t.TempDir(), "prune-me.pdf")
	if err := os.WriteFile(spoolPath, []byte("%PDF-1.4 x"), 0o600); err != nil {
		t.Fatalf("write spool file: %v", err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-prune",
		SpoolPath:      spoolPath,
		OrigFilename:   "x.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      10,
		SHA256:         "prune-sha",
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}
	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if err := db.MarkUploaded(ctx, res.ItemID, "uuid-prune", 1); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}

	d.PruneOnce(ctx)

	if _, err := os.Stat(spoolPath); !os.IsNotExist(err) {
		t.Errorf("spool file still exists after PruneOnce: stat err = %v", err)
	}
}

// captureLog is a concurrency-safe []string sink used as a Daemon.Logf.
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

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-55/NF-03: the PAT must never reach a log line, even on an error path."
func TestPATNeverReachesALogLineEvenOnUploadFailure(t *testing.T) {
	const secretPAT = "cf_extremely_secret_pat_do_not_leak_1234567890"
	t.Setenv("CF_TOKEN", secretPAT)

	gmailSrv := gwfake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.Labels = []*gmail.Label{{Id: "Label_1", Name: gmailwatch.SubmittedLabelName}}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) { return &gmail.Profile{HistoryId: 1}, nil }
	gmailSrv.HistoryFunc = func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error) {
		return &gmail.ListHistoryResponse{HistoryId: startHistoryID}, nil
	}

	cfSrv := cffake.New()
	defer cfSrv.Close()
	// 401: F-51's PAT-invalid path — the classic place a naive
	// implementation might be tempted to echo the Authorization header
	// into a log line while explaining the failure.
	cfSrv.Enqueue(cffake.ScriptedResponse{StatusCode: 401})

	fakeNotifier := &notify.Fake{}
	logs := &captureLog{}
	d, db := newTestDaemon(t, gmailSrv, cfSrv, fakeNotifier, logs.Logf)

	ctx := context.Background()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{GmailMessageID: "msg-401", From: "v@example.com", Subject: "s"}); err != nil {
		t.Fatalf("RecordMessageIfNew: %v", err)
	}
	spoolPath := filepath.Join(t.TempDir(), "fail.pdf")
	if err := os.WriteFile(spoolPath, []byte("%PDF-1.4 x"), 0o600); err != nil {
		t.Fatalf("write spool file: %v", err)
	}
	res, err := db.StageItem(ctx, queue.NewItem{
		GmailMessageID: "msg-401",
		SpoolPath:      spoolPath,
		OrigFilename:   "x.pdf",
		MimeType:       "application/pdf",
		SizeBytes:      10,
		SHA256:         "fail-sha",
	})
	if err != nil {
		t.Fatalf("StageItem: %v", err)
	}
	if err := db.Approve(ctx, res.ItemID, queue.ActorHuman); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	d.RunIteration(ctx)

	item, err := db.GetItem(ctx, res.ItemID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != queue.StatusFailed {
		t.Fatalf("item status after 401 = %q, want %q", item.Status, queue.StatusFailed)
	}

	// AC-24's mechanism: grep the entire captured log/notification corpus
	// for the raw PAT value.
	for _, line := range logs.All() {
		if strings.Contains(line, secretPAT) {
			t.Errorf("PAT leaked into a log line: %q", line)
		}
	}
	for _, msg := range fakeNotifier.All() {
		if strings.Contains(msg, secretPAT) {
			t.Errorf("PAT leaked into a notification: %q", msg)
		}
	}
	if item.LastError != "" && strings.Contains(item.LastError, secretPAT) {
		t.Errorf("PAT leaked into item.LastError: %q", item.LastError)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-65: Logs are local, rotated, and never contain message bodies or attachment contents. Subjects are logged; bodies are not."
func TestMessageBodyNeverReachesALogLine(t *testing.T) {
	// rfc2047-filename.eml's body is "Voir la facture ci-jointe." — distinct
	// from its subject "Votre facture" — so a leak of the body specifically
	// (not just any text from the message) is unambiguous.
	const bodyMarker = "Voir la facture ci-jointe"

	gmailSrv := gwfake.NewServer()
	defer gmailSrv.Close()
	gmailSrv.Labels = []*gmail.Label{{Id: "Label_1", Name: gmailwatch.SubmittedLabelName}}
	raw := rawFixtureMessage(t, "rfc2047-filename.eml")
	gmailSrv.MessagesListFunc = func(q, pageToken string) (*gmail.ListMessagesResponse, error) {
		return &gmail.ListMessagesResponse{Messages: []*gmail.Message{{Id: "msg-body-leak"}}}, nil
	}
	gmailSrv.MessagesGetFunc = func(id, format string) (*gmail.Message, error) {
		return &gmail.Message{Id: id, Raw: raw, InternalDate: time.Now().UnixMilli()}, nil
	}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) { return &gmail.Profile{HistoryId: 1}, nil }

	cfSrv := cffake.New()
	defer cfSrv.Close()

	logs := &captureLog{}
	d, _ := newTestDaemon(t, gmailSrv, cfSrv, &notify.Fake{}, logs.Logf)

	d.RunIteration(context.Background())

	for _, line := range logs.All() {
		if strings.Contains(line, bodyMarker) {
			t.Errorf("message body leaked into a log line: %q", line)
		}
	}
}
