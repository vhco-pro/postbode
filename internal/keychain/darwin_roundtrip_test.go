// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "`internal/keychain` (F-62) — `go-keyring` wrapper storing the ClearFacts PAT and Gmail token, **with an env-var fallback for dev** (`CF_TOKEN`)"
//go:build darwin

package keychain

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Regression guard for a real defect: security(1)'s interactive -w prompt is
// line-based and capped at 128 characters, so an earlier stdin-based writer
// silently truncated multi-line and long secrets and stored valid-looking
// garbage. The Gmail OAuth token is pretty-printed JSON across six lines and
// ~500 bytes and hit both limits at once.
//
// Touches the real login Keychain, so it is darwin-only and cleans up after
// itself. It writes under a zz- account no production code reads.
func TestDarwinStoreRoundTripsLongAndMultiLineSecrets(t *testing.T) {
	pretty, err := json.MarshalIndent(map[string]any{
		"access_token":  "ya29." + strings.Repeat("x", 200),
		"refresh_token": "1//" + strings.Repeat("r", 80),
		"token_type":    "Bearer",
		"expiry":        "2026-08-04T18:00:00Z",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"short single line (the PAT shape)":        strings.Repeat("c", 80),
		"longer than the 128 prompt cap":           strings.Repeat("L", 300),
		"multi-line pretty JSON (the token shape)": string(pretty),
	}

	ctx := context.Background()
	s := DarwinStore{}
	for name, secret := range cases {
		t.Run(name, func(t *testing.T) {
			const account = "zz-postbode-roundtrip-selftest"
			t.Cleanup(func() { _ = deleteForTest(ctx, account) })

			if err := s.Set(ctx, account, secret); err != nil {
				t.Fatalf("Set: %v", err)
			}
			got, err := s.Get(ctx, account)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != secret {
				t.Errorf("round-trip lost data: wrote %d bytes, read %d back", len(secret), len(got))
			}
		})
	}
}
