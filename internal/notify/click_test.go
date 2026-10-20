package notify

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// N1: launchd's PATH need not include Homebrew. Test both supported prefixes.
func TestHelperDiscoveryWithMinimalPath(t *testing.T) {
	for _, installed := range []string{"terminal-notifier", "/opt/homebrew/bin/terminal-notifier", "/usr/local/bin/terminal-notifier", ""} {
		t.Run(installed, func(t *testing.T) {
			got := helperPath(func(candidate string) (string, error) {
				if candidate == installed {
					return candidate, nil
				}
				return "", errors.New("not found")
			})
			if got != installed {
				t.Fatalf("helper = %q, want %q", got, installed)
			}
		})
	}
}

// N2: capture the actual helper arguments, then execute the click command
// against a harmless executable whose name would break unquoted shell code.
func TestClickRunsAbsoluteReviewWithoutNotificationSecrets(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "post bode's $(false)")
	output := filepath.Join(dir, "clicked")
	t.Setenv("CLICK_OUTPUT", output)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$CLICK_OUTPUT\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "terminal-notifier")
	argsFile := filepath.Join(dir, "args")
	t.Setenv("CAPTURE_ARGS", argsFile)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	n := BestReview(executable)
	if err := n.Notify(context.Background(), "Reconnect Gmail"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(args) != 6 || !reflect.DeepEqual(args[:5], []string{"-title", "Postbode", "-message", "Reconnect Gmail", "-execute"}) {
		t.Fatalf("helper arguments = %q", args)
	}
	if strings.Contains(args[5], "t=") {
		t.Fatal("session token in notification")
	}
	if err := exec.Command("/bin/sh", "-c", args[5]).Run(); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(output)
	if err != nil || string(b) != "review" {
		t.Fatalf("click = %q, %v", b, err)
	}
}
