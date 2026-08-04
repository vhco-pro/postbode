// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 10, Criterion: "Subcommand wiring for `postboded`, `postbode review`, `postbode status`, `postbode log [--since 24h]` on a single static binary (F-60)"
package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/webui"
)

// fakeLauncher records the URL it was asked to open instead of shelling out
// to the real `open` command — no browser is ever launched in tests.
type fakeLauncher struct {
	opened []string
	err    error
}

func (f *fakeLauncher) Open(url string) error {
	f.opened = append(f.opened, url)
	return f.err
}

func TestReviewOpensTokenizedURL(t *testing.T) {
	home := withTempHome(t)
	if err := webui.WriteTokenFile(webui.DefaultTokenPath(home), "test-session-token"); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	fake := &fakeLauncher{}
	orig := reviewLauncher
	reviewLauncher = fake
	t.Cleanup(func() { reviewLauncher = orig })

	var stdout, stderr bytes.Buffer
	code := run([]string{"review"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([review]) = %d, stderr = %q", code, stderr.String())
	}

	if len(fake.opened) != 1 {
		t.Fatalf("launcher.Open called %d times, want 1", len(fake.opened))
	}
	if !strings.Contains(fake.opened[0], "127.0.0.1:7391") || !strings.Contains(fake.opened[0], "t=test-session-token") {
		t.Errorf("opened URL = %q, want it to contain the review addr and token", fake.opened[0])
	}
}

// F-46/OQ-P8: a missing token file means the daemon isn't running (or has
// never started); `postbode review` must fail with a clear message rather
// than a bare os.ReadFile error, and must never launch a browser.
func TestReviewFailsClearlyWhenNoTokenFile(t *testing.T) {
	withTempHome(t) // no token file written

	fake := &fakeLauncher{}
	orig := reviewLauncher
	reviewLauncher = fake
	t.Cleanup(func() { reviewLauncher = orig })

	var stdout, stderr bytes.Buffer
	code := run([]string{"review"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("run([review]) = 0 with no token file, want non-zero")
	}
	if !strings.Contains(stderr.String(), "daemon running") {
		t.Errorf("review error = %q, want it to mention the daemon", stderr.String())
	}
	if len(fake.opened) != 0 {
		t.Errorf("launcher.Open called %d times, want 0", len(fake.opened))
	}
}
