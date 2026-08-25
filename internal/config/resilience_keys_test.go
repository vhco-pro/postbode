package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/config"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-47: A config.yaml containing all four new gmail: keys loads with those values"
func TestResilienceKeysLoad(t *testing.T) {
	path := writeConfig(t, `
administration:
  company_number: "0123456789"
gmail:
  watch: all
  failure_budget: 5
  park_retry_cooldown: 90m
  park_retry_attempts: 7
  poll_failure_budget: 2
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Gmail.FailureBudget != 5 {
		t.Errorf("FailureBudget = %d, want 5", cfg.Gmail.FailureBudget)
	}
	if cfg.Gmail.ParkRetryCooldown != 90*time.Minute {
		t.Errorf("ParkRetryCooldown = %s, want 90m", cfg.Gmail.ParkRetryCooldown)
	}
	if cfg.Gmail.ParkRetryAttempts != 7 {
		t.Errorf("ParkRetryAttempts = %d, want 7", cfg.Gmail.ParkRetryAttempts)
	}
	if cfg.Gmail.PollFailureBudget != 2 {
		t.Errorf("PollFailureBudget = %d, want 2", cfg.Gmail.PollFailureBudget)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-47: ... Omitting all four yields the documented defaults 3 / 6h / 3 / 3."
func TestResilienceKeysDefaultWhenOmitted(t *testing.T) {
	path := writeConfig(t, `
administration:
  company_number: "0123456789"
gmail:
  watch: all
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Gmail.FailureBudget != 3 {
		t.Errorf("FailureBudget = %d, want the documented default 3", cfg.Gmail.FailureBudget)
	}
	if cfg.Gmail.ParkRetryCooldown != 6*time.Hour {
		t.Errorf("ParkRetryCooldown = %s, want the documented default 6h", cfg.Gmail.ParkRetryCooldown)
	}
	if cfg.Gmail.ParkRetryAttempts != 3 {
		t.Errorf("ParkRetryAttempts = %d, want the documented default 3", cfg.Gmail.ParkRetryAttempts)
	}
	if cfg.Gmail.PollFailureBudget != 3 {
		t.Errorf("PollFailureBudget = %d, want the documented default 3", cfg.Gmail.PollFailureBudget)
	}

	// config.Default() must agree with the file-less path, or the daemon
	// behaves differently depending on whether a config exists.
	d := config.Default()
	if d.Gmail.FailureBudget != cfg.Gmail.FailureBudget ||
		d.Gmail.ParkRetryCooldown != cfg.Gmail.ParkRetryCooldown ||
		d.Gmail.ParkRetryAttempts != cfg.Gmail.ParkRetryAttempts ||
		d.Gmail.PollFailureBudget != cfg.Gmail.PollFailureBudget {
		t.Errorf("config.Default() disagrees with the defaults applied to a file: %+v vs %+v", d.Gmail, cfg.Gmail)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-47: ... one containing failure_budget: 0 (and, separately, park_retry_attempts: -1) is rejected at startup with the offending line number, and the daemon does not start."
func TestResilienceKeysRejectNonPositiveValuesWithLineNumbers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "failure_budget zero",
			// A budget of 0 would park every message on its first hiccup:
			// a G-1 hazard dressed as a config typo.
			body: "administration:\n  company_number: \"1\"\ngmail:\n  failure_budget: 0\n",
			want: "failure_budget",
		},
		{
			name: "park_retry_attempts negative",
			body: "administration:\n  company_number: \"1\"\ngmail:\n  park_retry_attempts: -1\n",
			want: "park_retry_attempts",
		},
		{
			name: "poll_failure_budget zero",
			body: "administration:\n  company_number: \"1\"\ngmail:\n  poll_failure_budget: 0\n",
			want: "poll_failure_budget",
		},
		{
			name: "park_retry_cooldown zero",
			body: "administration:\n  company_number: \"1\"\ngmail:\n  park_retry_cooldown: 0s\n",
			want: "park_retry_cooldown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(writeConfig(t, tc.body))
			if err == nil {
				t.Fatal("Load accepted a non-positive bound; the daemon must refuse to start")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not name the offending key %q", msg, tc.want)
			}
			// F-29: the message must carry the line number, so the fix is
			// obvious without hunting through the file.
			if !strings.Contains(msg, "line 4") {
				t.Errorf("error %q does not carry the offending line number (want line 4)", msg)
			}
		})
	}
}

// The allow-list coupling is easy to miss and fails in a way that reads like
// a user mistake ("unknown key"), so it gets its own guard.
func TestResilienceKeysAreOnTheAllowList(t *testing.T) {
	for _, key := range []string{"failure_budget", "park_retry_cooldown", "park_retry_attempts", "poll_failure_budget"} {
		t.Run(key, func(t *testing.T) {
			value := "3"
			if key == "park_retry_cooldown" {
				value = "1h"
			}
			body := "administration:\n  company_number: \"1\"\ngmail:\n  " + key + ": " + value + "\n"
			if _, err := config.Load(writeConfig(t, body)); err != nil {
				t.Fatalf("Load rejected %q: %v — the key is missing from decodeGmail's allow-list", key, err)
			}
		})
	}
}
