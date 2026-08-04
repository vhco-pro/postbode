package gmailwatch_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"golang.org/x/oauth2"
)

const fixtureCredentialsJSON = `{
  "installed": {
    "client_id": "test-client-id.apps.googleusercontent.com",
    "client_secret": "test-client-secret",
    "redirect_uris": ["http://localhost"],
    "auth_uri": "https://accounts.google.com/o/oauth2/auth",
    "token_uri": "https://oauth2.googleapis.com/token"
  }
}`

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "when `credentials.json` is missing the spike must **skip leg (d)** with a clear SKIPPED message and still exit 0"
func TestCredentialsFileExists(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "credentials.json")
	if gmailwatch.CredentialsFileExists(missing) {
		t.Error("CredentialsFileExists() = true for a file that was never written")
	}

	if err := os.WriteFile(missing, []byte(fixtureCredentialsJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !gmailwatch.CredentialsFileExists(missing) {
		t.Error("CredentialsFileExists() = false for a file that exists")
	}
}

func TestCredentialsFileExistsRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if gmailwatch.CredentialsFileExists(dir) {
		t.Error("CredentialsFileExists() = true for a directory, want false")
	}
}

func TestLoadOAuthConfigParsesDesktopCredentials(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte(fixtureCredentialsJSON), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := gmailwatch.LoadOAuthConfig(path)
	if err != nil {
		t.Fatalf("LoadOAuthConfig: %v", err)
	}
	if cfg.ClientID != "test-client-id.apps.googleusercontent.com" {
		t.Errorf("ClientID = %q, want the fixture client id", cfg.ClientID)
	}
	if len(cfg.Scopes) != 2 {
		t.Errorf("Scopes = %v, want the 2 gmailwatch.Scopes (readonly + modify)", cfg.Scopes)
	}
}

func TestLoadOAuthConfigMissingFile(t *testing.T) {
	if _, err := gmailwatch.LoadOAuthConfig(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("LoadOAuthConfig: want an error for a missing file, got nil")
	}
}

func TestTokenCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "token.json")

	want := &oauth2.Token{
		AccessToken:  "access-xyz",
		RefreshToken: "refresh-abc",
		TokenType:    "Bearer",
	}
	if err := gmailwatch.SaveToken(path, want); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("token cache file mode = %v, want 0600", got)
	}

	got, err := gmailwatch.LoadCachedToken(path)
	if err != nil {
		t.Fatalf("LoadCachedToken: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Errorf("LoadCachedToken() = %+v, want %+v", got, want)
	}
}

func TestLoadCachedTokenMissingFile(t *testing.T) {
	if _, err := gmailwatch.LoadCachedToken(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("LoadCachedToken: want an error for a missing file, got nil")
	}
}
