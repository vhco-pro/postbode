package keychain_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/clearfacts"
	"github.com/vhco-pro/postbode/internal/keychain"
	"golang.org/x/oauth2"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-62: Secrets (Gmail OAuth refresh token, ClearFacts PAT) stored via a Keychain-backed Store, with an env-var fallback for dev only."
func TestFakeStoreGetSetRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := keychain.NewFake()

	if _, err := store.Get(ctx, keychain.AccountClearFactsPAT); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("Get on empty store: got err %v, want ErrNotFound", err)
	}

	if err := store.Set(ctx, keychain.AccountClearFactsPAT, "cf_secret_value"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get(ctx, keychain.AccountClearFactsPAT)
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}
	if got != "cf_secret_value" {
		t.Errorf("Get after Set = %q, want %q", got, "cf_secret_value")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-62: ... with an env-var fallback for dev only (CF_TOKEN)."
func TestPATSourcePrefersEnvVarOverStore(t *testing.T) {
	ctx := context.Background()
	store := keychain.NewFake()
	store.Seed(keychain.AccountClearFactsPAT, "cf_from_store")

	t.Setenv(keychain.PATEnvVar, "cf_from_env")
	src := keychain.PATSource{Store: store}
	tok, err := src.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.Reveal() != "cf_from_env" {
		t.Errorf("Token() = %q, want env value %q", tok.Reveal(), "cf_from_env")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-62: Secrets ... stored via a Keychain-backed Store."
func TestPATSourceFallsBackToStoreWhenEnvUnset(t *testing.T) {
	ctx := context.Background()
	store := keychain.NewFake()
	store.Seed(keychain.AccountClearFactsPAT, "cf_from_store")
	_ = os.Unsetenv(keychain.PATEnvVar)

	src := keychain.PATSource{Store: store}
	tok, err := src.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.Reveal() != "cf_from_store" {
		t.Errorf("Token() = %q, want store value %q", tok.Reveal(), "cf_from_store")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-62: Secrets ... stored via a Keychain-backed Store."
func TestPATSourceErrorsWhenNeitherEnvNorStoreHasIt(t *testing.T) {
	ctx := context.Background()
	_ = os.Unsetenv(keychain.PATEnvVar)
	src := keychain.PATSource{Store: keychain.NewFake()}
	if _, err := src.Token(ctx); err == nil {
		t.Fatal("Token: got nil error, want an error when neither env nor store has the PAT")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-62: Secrets (Gmail OAuth refresh token, ClearFacts PAT) stored via a Keychain-backed Store."
func TestSaveAndLoadGmailTokenRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := keychain.NewFake()

	want := &oauth2.Token{
		AccessToken:  "access-123",
		RefreshToken: "refresh-456",
		TokenType:    "Bearer",
		Expiry:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := keychain.SaveGmailToken(ctx, store, want); err != nil {
		t.Fatalf("SaveGmailToken: %v", err)
	}

	got, err := keychain.LoadGmailToken(ctx, store)
	if err != nil {
		t.Fatalf("LoadGmailToken: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Errorf("LoadGmailToken = %+v, want %+v", got, want)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-16: routine re-auth must be distinguishable from a genuinely absent secret."
func TestLoadGmailTokenReturnsErrNotFoundBeforeFirstAuth(t *testing.T) {
	ctx := context.Background()
	store := keychain.NewFake()
	if _, err := keychain.LoadGmailToken(ctx, store); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatalf("LoadGmailToken on empty store: got err %v, want ErrNotFound", err)
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 13, Criterion: "F-55/NF-03: the PAT must never reach a log line, even on an error path."
func TestPATNeverAppearsUnredactedInAnErrorString(t *testing.T) {
	ctx := context.Background()
	store := keychain.NewFake()
	const secret = "cf_super_secret_pat_value_should_never_leak"
	store.Seed(keychain.AccountClearFactsPAT, secret)
	_ = os.Unsetenv(keychain.PATEnvVar)

	src := keychain.PATSource{Store: store}
	tok, err := src.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}

	// clearfacts.Token's Stringer/Format/GoString always redact — this is
	// what every logging call site in the codebase relies on. Prove it
	// here at the boundary where the raw secret first becomes a
	// clearfacts.Token.
	rendered := errors.New("wrapped: " + tokenString(tok)).Error()
	if rendered != "wrapped: cf_***" {
		t.Errorf("token rendered as %q, want the redacted form", rendered)
	}
}

func tokenString(t clearfacts.Token) string {
	return t.String()
}
