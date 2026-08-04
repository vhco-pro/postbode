package launchd_test

import (
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/launchd"
)

const plistPath = "be.vhco.postbode.plist"

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "`launchd/be.vhco.postbode.plist` with `KeepAlive`, installed by `make install-launchagent` (F-61, AC-25)"
//
// This test proves the plist's STRUCTURE — the machine-verifiable half of
// AC-25. The runtime half ("after `make install-launchagent`, `launchctl
// list` shows the agent, and `kill -9` results in an automatic restart
// with the queue intact") requires installing a real LaunchAgent into a
// real user's ~/Library/LaunchAgents and is explicitly NOT exercised here
// per this phase's own instructions — it is human-verifiable only.
func TestPlistParsesAsValidXML(t *testing.T) {
	if _, err := launchd.Parse(plistPath); err != nil {
		t.Fatalf("Parse(%s): %v", plistPath, err)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "`launchd/be.vhco.postbode.plist` with `KeepAlive`, installed by `make install-launchagent` (F-61, AC-25)"
func TestPlistHasLabel(t *testing.T) {
	dict, err := launchd.Parse(plistPath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, ok := dict["Label"]
	if !ok || got.Str != "be.vhco.postbode" {
		t.Errorf("Label = %+v, want the string \"be.vhco.postbode\"", got)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "`launchd/be.vhco.postbode.plist` with `KeepAlive` ..."
func TestPlistHasKeepAliveAndRunAtLoadTrue(t *testing.T) {
	dict, err := launchd.Parse(plistPath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, key := range []string{"KeepAlive", "RunAtLoad"} {
		v, ok := dict[key]
		if !ok {
			t.Errorf("%s missing from plist", key)
			continue
		}
		if !v.BoolSet || !v.BoolVal {
			t.Errorf("%s = %+v, want a bare <true/>", key, v)
		}
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "AC-25: After `make install-launchagent`, `launchctl list` shows the agent, and `kill -9` of the daemon results in an automatic restart with the queue intact."
func TestPlistProgramArgumentsInvokeDaemonSubcommand(t *testing.T) {
	dict, err := launchd.Parse(plistPath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	args, ok := dict["ProgramArguments"]
	if !ok || len(args.Array) < 2 {
		t.Fatalf("ProgramArguments = %+v, want at least [binary, \"daemon\"]", args)
	}
	if args.Array[len(args.Array)-1] != "daemon" {
		t.Errorf("ProgramArguments last element = %q, want \"daemon\"", args.Array[len(args.Array)-1])
	}
	if args.Array[0] == "" || args.Array[0][0] != '/' {
		t.Errorf("ProgramArguments[0] = %q, want an absolute path (launchd does not resolve $PATH)", args.Array[0])
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-65: Logs are local, rotated ..."
func TestPlistLogPathsAreUnderLibraryLogsPostbode(t *testing.T) {
	dict, err := launchd.Parse(plistPath)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, key := range []string{"StandardOutPath", "StandardErrorPath"} {
		v, ok := dict[key]
		if !ok || v.Str == "" {
			t.Fatalf("%s missing or empty", key)
		}
		if !strings.Contains(v.Str, "/Library/Logs/Postbode/") {
			t.Errorf("%s = %q, want a path under .../Library/Logs/Postbode/", key, v.Str)
		}
	}
}
