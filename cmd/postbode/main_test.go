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
	withTempHome(t)
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{name: "no args prints usage", args: nil, wantCode: 2, wantOut: "usage: postbode"},
		{name: "help exits zero", args: []string{"--help"}, wantCode: 0, wantOut: "usage: postbode"},
		{name: "unknown command", args: []string{"frobnicate"}, wantCode: 2, wantOut: `unknown command "frobnicate"`},
		{name: "daemon without credentials degrades gracefully", args: []string{"daemon"}, wantCode: 0, wantOut: "not yet authenticated"},
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
