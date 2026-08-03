package clearfacts

import (
	"context"
	"fmt"
	"io"
	"os"
)

// redactedToken is what a Token must print as everywhere: log lines, error
// strings, %v/%s/%q formatting. See F-55.
const redactedToken = "cf_***"

// Token is a ClearFacts personal access token. Its zero-value formatting
// methods make accidental leakage into a log line or an error string
// impossible — only Reveal() returns the real value, and that method exists
// solely to build the Authorization header.
type Token string

// String implements fmt.Stringer. Always returns the redacted form.
func (t Token) String() string { return redactedToken }

// GoString implements fmt.GoStringer (the %#v path). Always returns the
// redacted form.
func (t Token) GoString() string { return redactedToken }

// Format implements fmt.Formatter, covering every verb (%v, %s, %q, ...).
// Always writes the redacted form regardless of verb or flags.
func (t Token) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, redactedToken)
}

// Reveal returns the real token value. The only legitimate caller is the
// request-signing code in client.go; nothing else in this package or its
// callers should ever call it for logging or error construction.
func (t Token) Reveal() string { return string(t) }

// TokenSource supplies the PAT for every request. The env-var implementation
// (EnvTokenSource) is used in dev; a macOS Keychain-backed implementation
// lands in Phase 13 (F-62) behind the same interface.
type TokenSource interface {
	Token(ctx context.Context) (Token, error)
}

// TokenSourceFunc adapts a plain function to TokenSource — mainly useful in
// tests.
type TokenSourceFunc func(ctx context.Context) (Token, error)

// Token implements TokenSource.
func (f TokenSourceFunc) Token(ctx context.Context) (Token, error) { return f(ctx) }

// EnvTokenSource reads the PAT from an environment variable (CF_TOKEN by
// default). This is the dev-mode implementation of F-55; Phase 13 adds a
// Keychain-backed TokenSource for production without changing this
// interface.
type EnvTokenSource struct {
	// EnvVar names the environment variable to read. Defaults to CF_TOKEN
	// when empty.
	EnvVar string
}

// Token implements TokenSource.
func (s EnvTokenSource) Token(_ context.Context) (Token, error) {
	name := s.EnvVar
	if name == "" {
		name = "CF_TOKEN"
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("clearfacts: environment variable %s is not set", name)
	}
	return Token(v), nil
}
