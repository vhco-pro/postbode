// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "**Token handoff to `postbode review`**: the daemon writes the token to `~/Library/Application Support/Postbode/session.token` at mode `0600` on start; `postbode review` reads it and opens the tokenized URL."
package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/webui"
)

type recordingLauncher struct {
	urls []string
}

func (l *recordingLauncher) Open(url string) error {
	l.urls = append(l.urls, url)
	return nil
}

func TestReviewOpensURLBuiltFromTokenFile(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "session.token")
	if err := webui.WriteTokenFile(tokenPath, "sekrit-token"); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	l := &recordingLauncher{}
	if err := cli.Review(tokenPath, webui.DefaultAddr, l); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(l.urls) != 1 {
		t.Fatalf("launcher.Open called %d times, want 1", len(l.urls))
	}
	if !strings.Contains(l.urls[0], "sekrit-token") || !strings.Contains(l.urls[0], webui.DefaultAddr) {
		t.Errorf("opened URL = %q, want it to contain the token and %s", l.urls[0], webui.DefaultAddr)
	}
}

func TestReviewErrorsWithoutLaunchingWhenTokenFileMissing(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "does-not-exist", "session.token")

	l := &recordingLauncher{}
	err := cli.Review(tokenPath, webui.DefaultAddr, l)
	if err == nil {
		t.Fatal("Review: got nil error for a missing token file, want an error")
	}
	if !strings.Contains(err.Error(), "daemon running") {
		t.Errorf("Review error = %q, want it to mention the daemon", err.Error())
	}
	if len(l.urls) != 0 {
		t.Errorf("launcher.Open called %d times, want 0", len(l.urls))
	}
}

func TestReviewURLIsQueryEscaped(t *testing.T) {
	got := cli.ReviewURL("127.0.0.1:7391", "a b&c")
	if !strings.Contains(got, "a+b%26c") {
		t.Errorf("ReviewURL = %q, want the token query-escaped", got)
	}
}
