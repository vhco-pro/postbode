package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "Never exit non-zero on a recoverable condition."
func TestRunDaemonWithoutCredentialsExitsZeroWithActionableMessage(t *testing.T) {
	withTempHome(t)

	// This package's directory (the test's cwd) never carries a
	// credentials.json, so buildGmailService takes the "not authenticated
	// yet" branch before ever constructing a keychain.DarwinStore — no
	// test in this repo may touch the real macOS Keychain.
	if _, err := os.Stat("credentials.json"); err == nil {
		t.Fatal("cmd/postbode unexpectedly has a credentials.json in its own directory — this test's no-Keychain-touch guarantee depends on it not existing")
	}

	var stdout, stderr bytes.Buffer
	got := run([]string{"daemon"}, &stdout, &stderr)
	if got != 0 {
		t.Errorf("run([daemon]) exit code = %d, want 0 (a missing bootstrap credential is not a crash-loop trigger)", got)
	}
	if !strings.Contains(stderr.String(), "not yet authenticated") {
		t.Errorf("stderr = %q, want an actionable \"not yet authenticated\" message", stderr.String())
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-60: Single static Go binary with subcommands: postboded (daemon), postbode review, postbode status, postbode log."
func TestDaemonDispatchIsWired(t *testing.T) {
	withTempHome(t)
	var stdout, stderr bytes.Buffer
	// A recognized, wired subcommand must never fall through to the
	// "unknown command" branch, and must never claim to be unimplemented.
	got := run([]string{"daemon"}, &stdout, &stderr)
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "not implemented yet") {
		t.Errorf("run([daemon]) output still claims to be unimplemented: %q", combined)
	}
	if strings.Contains(combined, "unknown command") {
		t.Errorf("run([daemon]) output = %q, want the daemon dispatch case, not \"unknown command\"", combined)
	}
	_ = got
}
