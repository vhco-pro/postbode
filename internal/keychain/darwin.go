package keychain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// securityPath is the fixed, absolute path to the macOS security(1) CLI —
// never resolved via $PATH, so a malicious PATH entry cannot intercept a
// secret read/write.
const securityPath = "/usr/bin/security"

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
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// Set stores value as the generic password for account, overwriting
// whatever was there before (-U — update if the item already exists,
// which is what every re-issued token / rotated PAT needs).
//
// The secret is fed on STDIN, never as an argv element.
//
// `security add-generic-password ... -w <value>` would be shorter, but a
// process's argv is world-readable on macOS: any local process could run
// `ps aux` during the write window and read the PAT in clear text. That
// would defeat the entire point of storing it in the Keychain (F-55,
// NF-03), so the extra handling here is load-bearing rather than
// fastidious.
//
// With -w and no value, security(1) prompts twice ("password data for new
// item" / "retype password for new item"), so the value is written twice.
// Verified against /usr/bin/security on macOS 26.4.
func (DarwinStore) Set(ctx context.Context, account, value string) error {
	cmd := exec.CommandContext(ctx, securityPath, "add-generic-password",
		"-U", "-s", Service, "-a", account, "-w")
	cmd.Stdin = strings.NewReader(value + "\n" + value + "\n")
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
