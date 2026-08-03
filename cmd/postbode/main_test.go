// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 1,
// Criterion: "`Makefile` with `build`, `test`, `vet`, `spike`, `e2e-dry`,
// `test-nonet`, `install-launchagent` targets; `test` and `vet` must pass on the
// empty tree (NF-12)"
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDispatch(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{name: "no args prints usage", args: nil, wantCode: 2, wantOut: "usage: postbode"},
		{name: "help exits zero", args: []string{"--help"}, wantCode: 0, wantOut: "usage: postbode"},
		{name: "unknown command", args: []string{"frobnicate"}, wantCode: 2, wantOut: `unknown command "frobnicate"`},
		{name: "known but unimplemented", args: []string{"status"}, wantCode: 1, wantOut: "not implemented yet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tt.args, &stdout, &stderr)
			if got != tt.wantCode {
				t.Errorf("run(%q) exit code = %d, want %d", tt.args, got, tt.wantCode)
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, tt.wantOut) {
				t.Errorf("run(%q) output = %q, want it to contain %q", tt.args, combined, tt.wantOut)
			}
		})
	}
}

// A known subcommand must never silently succeed while unimplemented — that
// would read as "the daemon ran" to launchd's KeepAlive.
func TestUnimplementedCommandsFailLoudly(t *testing.T) {
	for _, cmd := range []string{"daemon", "review", "status", "log"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{cmd}, &stdout, &stderr); code == 0 {
			t.Errorf("run([%q]) exited 0 while unimplemented; must be non-zero", cmd)
		}
		if stderr.Len() == 0 {
			t.Errorf("run([%q]) wrote nothing to stderr", cmd)
		}
	}
}
