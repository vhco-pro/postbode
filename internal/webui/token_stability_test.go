// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "**Session token (F-46, ratified v1.3)**: a random per-daemon-start token required on **every mutating request**"
package webui_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/webui"
)

// Regenerating the token on every start invalidated every bookmark and open
// tab on each restart — and launchd restarts the daemon on every upgrade,
// reboot or crash — while protecting nothing extra, since the value lives in
// the same 0600 file either way.
func TestLoadOrCreateTokenIsStableAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.token")

	first, err := webui.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("first call returned an empty token")
	}
	second, err := webui.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("token changed across restarts (%q -> %q): every bookmark would break", first, second)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// Deleting the file is the documented way to rotate a leaked token.
func TestDeletingTheTokenFileRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.token")
	first, _ := webui.LoadOrCreateToken(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	second, err := webui.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("token did not rotate after the file was deleted; there would be no way to revoke a leaked token")
	}
}

// A stray trailing newline (an editor, or `echo > session.token`) must not
// silently break authentication for the whole UI.
func TestTokenFileTrailingNewlineIsTolerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.token")
	if err := os.WriteFile(path, []byte("abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := webui.LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc123" {
		t.Errorf("got %q, want %q — trailing whitespace must be trimmed", got, "abc123")
	}
}
