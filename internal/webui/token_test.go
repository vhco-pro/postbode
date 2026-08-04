package webui_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/webui"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "Token handoff to `postbode review`: the daemon writes the token to `~/Library/Application Support/Postbode/session.token` at mode `0600` on start; `postbode review` reads it and opens the tokenized URL. The spec does not say how the separate CLI process learns the token (OQ-P8)"
func TestWriteTokenFileIsMode0600AndReadable(t *testing.T) {
	tmpHome := t.TempDir() // never the real home dir
	path := webui.DefaultTokenPath(tmpHome)

	tok, err := webui.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if tok == "" {
		t.Fatal("GenerateToken returned an empty token")
	}

	if err := webui.WriteTokenFile(path, tok); err != nil {
		t.Fatalf("WriteTokenFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 0600", perm)
	}

	got, err := webui.ReadTokenFile(path)
	if err != nil {
		t.Fatalf("ReadTokenFile: %v", err)
	}
	if got != tok {
		t.Errorf("ReadTokenFile = %q, want %q", got, tok)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "Random per-daemon-start session token required on every mutating request; 401 without it (F-46, AC-21)"
func TestWriteTokenFileRewritesOnEveryStart(t *testing.T) {
	tmpHome := t.TempDir()
	path := webui.DefaultTokenPath(tmpHome)

	first, err := webui.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := webui.WriteTokenFile(path, first); err != nil {
		t.Fatalf("WriteTokenFile(first): %v", err)
	}

	second, err := webui.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if second == first {
		t.Fatal("two consecutive GenerateToken calls produced the same token")
	}
	if err := webui.WriteTokenFile(path, second); err != nil {
		t.Fatalf("WriteTokenFile(second): %v", err)
	}

	got, err := webui.ReadTokenFile(path)
	if err != nil {
		t.Fatalf("ReadTokenFile: %v", err)
	}
	if got != second {
		t.Errorf("ReadTokenFile = %q, want the rewritten token %q (stale token from a previous run must stop working)", got, second)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "Token handoff to `postbode review`: the daemon writes the token to `~/Library/Application Support/Postbode/session.token` at mode `0600` on start (OQ-P8)"
func TestDefaultTokenPathShape(t *testing.T) {
	got := webui.DefaultTokenPath("/Users/testuser")
	want := filepath.Join("/Users/testuser", "Library", "Application Support", "Postbode", "session.token")
	if got != want {
		t.Errorf("DefaultTokenPath = %q, want %q", got, want)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Security invariant (CLAUDE.md / spec), Criterion: "Compare tokens with `crypto/subtle.ConstantTimeCompare`"
func TestNewServerRejectsEmptyToken(t *testing.T) {
	db, _ := openTestDB(t)
	if _, err := webui.NewServer(db, ""); err == nil {
		t.Fatal("NewServer(db, \"\") succeeded, want an error — an empty token would mean no auth required")
	}
}
