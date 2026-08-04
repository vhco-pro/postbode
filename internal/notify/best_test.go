// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "**`internal/notify`** — macOS notification via `osascript`, **behind an interface with a fake**, so tests never shell out (F-45)"
package notify

import (
	"strings"
	"testing"
)

// osascript posts notifications as Script Editor, so clicking "Show" opens
// Script Editor rather than the review queue — reported as "why is it not
// opening the web UI?". terminal-notifier is a real app bundle and supports
// -open, so it is preferred when present.
func TestBestPrefersTerminalNotifierButAlwaysReturnsSomething(t *testing.T) {
	n := Best("http://127.0.0.1:7391/")
	if n == nil {
		t.Fatal("Best returned nil; notifications must never be silently dropped")
	}
	switch v := n.(type) {
	case TerminalNotifier:
		if v.OpenURL == "" {
			t.Error("TerminalNotifier has no OpenURL; the click would do nothing")
		}
		if !strings.HasPrefix(v.Path, "/") {
			t.Errorf("Path %q is not absolute", v.Path)
		}
		if strings.Contains(v.OpenURL, "t=") {
			t.Error("OpenURL carries a session token; Notification Center would persist it for no benefit")
		}
	case OSAScript:
		// Correct fallback when terminal-notifier is not installed.
	default:
		t.Errorf("Best returned unexpected %T", v)
	}
}

// The fallback must exist even with no helper installed: osascript ships
// with macOS, so a notification is never lost entirely.
func TestOSAScriptIsAlwaysAViableFallback(t *testing.T) {
	var _ Notifier = OSAScript{}
	var _ Notifier = TerminalNotifier{}
}
