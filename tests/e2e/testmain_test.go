// Package e2e_test drives Postbode's full pipeline — poll → extract →
// rules → stage → notify → (real HTTP) approve → upload → verify → label —
// against two httptest fakes (Gmail, ClearFacts) and real internal/queue,
// internal/extract, internal/rules, internal/uploader and internal/webui
// components, exactly as cmd/postbode's daemon wiring assembles them in
// production (ADR-002, decisions/ADR-002-go-native-e2e-without-a-browser-runner.md).
//
// Per ADR-002, the review UI is exercised with real net/http form POSTs
// against the actual webui.Server handler served on a real (loopback) TCP
// listener — no browser, no Playwright, no JS toolchain. F-42 ships zero
// client-side JavaScript, so a form POST is behaviourally identical to a
// browser click.
package e2e_test

import (
	"os"
	"testing"

	"github.com/vhco-pro/postbode/internal/testsupport"
)

// TestMain installs the AC-22 network guard before any test in this
// package runs. InstallNetworkGuard is a no-op unless
// POSTBODE_TEST_NO_NETWORK=1 is set (make test-nonet's job) — running
// plain `go test ./tests/e2e/...` (make e2e-dry) is unaffected.
func TestMain(m *testing.M) {
	testsupport.InstallNetworkGuard()
	os.Exit(m.Run())
}
