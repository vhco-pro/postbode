package keychain

import (
	"context"
	"sync"
)

// Fake is an in-memory Store — the only Store any test in this repo may
// use. No test may touch the real macOS Keychain (this phase's own
// instructions, mirroring notify.Fake and NF-09's "no live API" rule for
// external state in general).
type Fake struct {
	mu     sync.Mutex
	values map[string]string
}

// NewFake returns an empty Fake store.
func NewFake() *Fake {
	return &Fake{values: make(map[string]string)}
}

// Get implements Store.
func (f *Fake) Get(_ context.Context, account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[account]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

// Set implements Store.
func (f *Fake) Set(_ context.Context, account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[account] = value
	return nil
}

// Seed pre-populates account with value, bypassing Set — a convenience for
// tests that want a store already primed with a secret before the code
// under test ever calls Set.
func (f *Fake) Seed(account, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[account] = value
}
