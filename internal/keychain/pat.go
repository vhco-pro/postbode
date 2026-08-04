package keychain

import (
	"context"
	"fmt"
	"os"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// PATEnvVar is the F-62 dev-only fallback environment variable for the
// ClearFacts PAT — the same variable clearfacts.EnvTokenSource reads by
// default, so a .env-sourced dev shell keeps working unchanged once the
// daemon is wired through PATSource instead.
const PATEnvVar = "CF_TOKEN"

// PATSource implements clearfacts.TokenSource (migrating the Phase 2 PAT
// interface onto the Keychain, F-62): PATEnvVar, when set, always wins —
// dev override, matching clearfacts.EnvTokenSource's exact behaviour —
// otherwise the PAT is read from Store under AccountClearFactsPAT.
type PATSource struct {
	Store Store
}

// Token implements clearfacts.TokenSource.
func (s PATSource) Token(ctx context.Context) (clearfacts.Token, error) {
	if v := os.Getenv(PATEnvVar); v != "" {
		return clearfacts.Token(v), nil
	}
	v, err := s.Store.Get(ctx, AccountClearFactsPAT)
	if err != nil {
		return "", fmt.Errorf("keychain: PAT: %w", err)
	}
	return clearfacts.Token(v), nil
}

// SavePAT stores token under AccountClearFactsPAT. Used by whatever
// operator step first mints/rotates the PAT (out of this phase's scope —
// see the postpone note on `postbode doctor`, P4); provided here so that
// step has a documented, tested place to land.
func SavePAT(ctx context.Context, store Store, token clearfacts.Token) error {
	return store.Set(ctx, AccountClearFactsPAT, token.Reveal())
}
