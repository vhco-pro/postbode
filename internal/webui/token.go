package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// GenerateToken returns a fresh random session token (F-46): 32 bytes of
// crypto/rand, base64url-encoded (no padding) so it survives round-tripping
// through a URL query string untouched.
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("webui: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// DefaultTokenPath returns the F-46 default session-token path under the
// given home directory: "~/Library/Application Support/Postbode/session.token".
// homeDir is a parameter (never read from the environment here) so callers
// — production and tests alike — control it explicitly; tests must never
// touch the real home directory.
func DefaultTokenPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "Application Support", "Postbode", "session.token")
}

// WriteTokenFile writes token to path at mode 0600 (F-46), creating any
// missing parent directories. It is meant to be called once per daemon
// start, rewriting whatever token (if any) was there before — a stale
// token from a previous run must stop working immediately.
func WriteTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("webui: write token file: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return fmt.Errorf("webui: write token file: %w", err)
	}
	// os.WriteFile's mode argument is subject to umask; make 0600 explicit
	// in case umask loosened it, since F-46 requires the file to be
	// readable only by the owning UID.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("webui: write token file: chmod: %w", err)
	}
	return nil
}

// ReadTokenFile reads back a token written by WriteTokenFile. This is the
// F-46/OQ-P8 handoff mechanism: `postbode review`, a separate process from
// the daemon, calls this to learn the daemon's current session token and
// build the tokenized review URL.
func ReadTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("webui: read token file: %w", err)
	}
	return string(b), nil
}

// tokensEqual reports whether got matches want using a constant-time
// comparison (crypto/subtle), so a mutating request cannot be used to
// brute-force the session token via timing. An empty want never matches
// anything, including an empty got, since an empty configured token would
// mean "no auth required" — that must never happen.
func tokensEqual(want, got string) bool {
	if want == "" || len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}
