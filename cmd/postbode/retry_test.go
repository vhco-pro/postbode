package main

import (
	"bytes"
	"strings"
	"testing"
)

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-41: ... postbode retry with neither an id nor --all is a usage error, exit code 2, and changes nothing; retry <id> --all → usage, exit 2."
//
// The 2-vs-1 split is the point: 2 means "you typed something wrong and
// nothing happened", 1 means "the command was well formed and could not be
// carried out". A monitoring script can tell those apart.
func TestRetryExitCodes(t *testing.T) {
	withTempHome(t)

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{
			name:     "no argument is a usage error",
			args:     []string{"retry"},
			wantCode: 2,
			wantOut:  "specify a message id or --all",
		},
		{
			name:     "id and --all together is a usage error",
			args:     []string{"retry", "--all", "msg-a"},
			wantCode: 2,
			wantOut:  "specify a message id or --all",
		},
		{
			name:     "more than one id is a usage error",
			args:     []string{"retry", "msg-a", "msg-b"},
			wantCode: 2,
			wantOut:  "expected at most one message id",
		},
		{
			name:     "unknown id fails, not a usage error",
			args:     []string{"retry", "msg-nope"},
			wantCode: 1,
			wantOut:  "msg-nope",
		},
		{
			name:     "--all with nothing parked succeeds",
			args:     []string{"retry", "--all"},
			wantCode: 0,
			wantOut:  "no parked messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(tt.args, &stdout, &stderr)
			if got != tt.wantCode {
				t.Errorf("run(%q) exit code = %d, want %d (stderr: %s)", tt.args, got, tt.wantCode, stderr.String())
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, tt.wantOut) {
				t.Errorf("run(%q) output = %q, want it to contain %q", tt.args, combined, tt.wantOut)
			}
		})
	}
}

// The verb has to be discoverable: a parked message is only ever surfaced
// by a notification and by `postbode status`, both of which name this
// command.
func TestRetryAppearsInUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	run([]string{"--help"}, &stdout, &stderr)
	out := stdout.String() + stderr.String()
	for _, want := range []string{"retry <message-id>", "retry --all"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage does not document %q:\n%s", want, out)
		}
	}
}
