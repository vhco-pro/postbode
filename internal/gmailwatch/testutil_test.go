package gmailwatch_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/vhco-pro/postbode/internal/config"
	"github.com/vhco-pro/postbode/internal/extract"
	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/queue"
	"github.com/vhco-pro/postbode/internal/rules"
)

// fixtureDir is the repo-root testdata/ directory shared across phases,
// reached relative to this package's directory since `go test` runs with
// the package dir as its cwd (mirrors internal/extract/testutil_test.go).
const fixtureDir = "../../testdata"

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatalf("loadFixture(%s): %v", name, err)
	}
	return data
}

// rawMessage base64url-encodes an .eml fixture the way Gmail's
// messages.get(format=raw) would return it.
func rawMessage(t *testing.T, fixtureName string) string {
	t.Helper()
	return base64.URLEncoding.EncodeToString(loadFixture(t, fixtureName))
}

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

func spoolDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "spool")
}

// gmailServiceFor builds a *gmail.Service pointed at fk's httptest server,
// with no OAuth transport in the way — mirrors labels_test.go's
// newFakeGmailService helper.
func gmailServiceFor(t *testing.T, fk *fake.Server) *gmail.Service {
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

// newTestWatcher wires a Watcher against a fresh queue.DB, a real
// extract.Extractor + rules.Engine (config.Default()'s ruleset — no
// explicit rules, so everything with a PDF attachment queues by default),
// and svc — exactly the "wire the pipeline" shape gmailwatch is
// responsible for (poll -> extract -> rules -> stage).
func newTestWatcher(t *testing.T, svc *gmail.Service, db *queue.DB) *gmailwatch.Watcher {
	t.Helper()
	return &gmailwatch.Watcher{
		Service:   svc,
		DB:        db,
		Extractor: extract.New(spoolDir(t), db),
		Rules:     rules.New(config.Default()),
		UserID:    "me",
		Config:    gmailwatch.Config{QueryWindowDays: 30},
	}
}
