package keychain

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// securityPath is the fixed, absolute path to the macOS security(1) CLI —
// never resolved via $PATH, so a malicious PATH entry cannot intercept a
// secret read/write.
const securityPath = "/usr/bin/security"

// valuePrefix tags values this package wrote, so Get knows to base64-decode
// them and leaves anything else alone.
//
// Encoding is not obfuscation, it is what keeps reads unambiguous.
// `security find-generic-password -w` prints the password as HEX whenever it
// contains a non-printable byte, with no marker distinguishing that from a
// secret that just happens to be hex digits. The Gmail OAuth token is
// multi-line JSON, so it came back as 794 hex characters for a 397-byte
// value. Base64 keeps every stored value printable, so -w always echoes it
// verbatim.
const valuePrefix = "pb1:"

// DarwinStore is the production Store (F-62): every secret is a macOS
// Keychain "generic password" item, service=Service, account=<Account*>,
// value=the secret. Reads and writes shell out to /usr/bin/security rather
// than a cgo Keychain binding, keeping NF-01's no-cgo constraint intact.
type DarwinStore struct{}

// Get reads the generic password for account, returning ErrNotFound when
// `security find-generic-password` reports no matching item — the normal,
// expected shape of "not authenticated yet", never surfaced as an
// operational error by callers.
func (DarwinStore) Get(ctx context.Context, account string) (string, error) {
	cmd := exec.CommandContext(ctx, securityPath, "find-generic-password",
		"-s", Service, "-a", account, "-w")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(stderr.String(), "could not be found") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain: find-generic-password(%s): %w", account, err)
	}
	raw := strings.TrimRight(stdout.String(), "\n")

	// Set writes valuePrefix+base64 (see its doc). The prefix makes decoding
	// unambiguous: a plain value that happens to be valid base64 — an 80-char
	// PAT easily could be — is returned untouched rather than silently
	// mangled. Values written by an older build, or by hand with
	// security(1), therefore still read correctly.
	if enc, ok := strings.CutPrefix(raw, valuePrefix); ok {
		decoded, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return "", fmt.Errorf("keychain: %s: stored value has the %q prefix but is not valid base64: %w", account, valuePrefix, err)
		}
		return string(decoded), nil
	}
	return raw, nil
}

// Set stores value as the generic password for account, overwriting
// whatever was there before (-U — update if the item already exists,
// which is what every re-issued token / rotated PAT needs).
//
// The value is passed with -w <value>. An earlier version of this code fed
// it on stdin instead, on the theory that argv is world-readable — that is
// true on Linux, but NOT on macOS, where a process's arguments are visible
// only to the same user or root. That is the same trust boundary as the
// 0600 token file in Application Support and as the login keychain itself,
// so stdin bought no real protection.
//
// It cost correctness, though. security(1)'s -w prompt is an interactive
// password prompt: line-based AND capped at 128 characters. A multi-line or
// long secret was silently truncated and stored as valid-looking garbage.
// The Gmail OAuth token is pretty-printed JSON across six lines and ~500
// bytes, and failed both ways at once — the daemon wrote one truncated line
// and then reported "unexpected end of JSON input" on every read, which
// looked like a corrupt token rather than a broken writer.
//
// Verified against /usr/bin/security on macOS 26.4: the argv path
// round-trips 300-character and multi-line values exactly; the stdin prompt
// truncates at 128.
func (DarwinStore) Set(ctx context.Context, account, value string) error {
	encoded := valuePrefix + base64.StdEncoding.EncodeToString([]byte(value))
	cmd := exec.CommandContext(ctx, securityPath, "add-generic-password",
		"-U", "-s", Service, "-a", account, "-w", encoded)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// stderr carries security(1)'s prompt text, never the secret, but
		// scrub defensively: an error path is exactly where a credential
		// tends to escape.
		msg := strings.ReplaceAll(stderr.String(), value, "<redacted>")
		return fmt.Errorf("keychain: add-generic-password(%s): %w: %s", account, err, msg)
	}
	return nil
}
