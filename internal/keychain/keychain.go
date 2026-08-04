// Package keychain stores Postbode's two secrets — the ClearFacts PAT and
// the Gmail OAuth token — in the macOS Keychain (F-62, NF-03), with an
// env-var fallback for dev only (CF_TOKEN for the PAT).
//
// # Why not github.com/zalando/go-keyring
//
// The plan names go-keyring as the intended wrapper target and NF-13
// ("no new dependencies") assumes it is already vendored. It is not: this
// repo's go.mod (checked at implementation time) carries none of
// go-keyring's transitive deps, and this phase is explicitly barred from
// touching go.mod/go.sum. Adding an unvendored import would either fail
// the build or silently require exactly the go.mod edit this phase must
// not make. Rather than violate the "don't touch go.mod" instruction or
// ship a build that doesn't compile, Store is implemented by shelling out
// to the `/usr/bin/security` CLI (part of every macOS install, zero new
// Go dependency) — behaviourally equivalent to go-keyring's own darwin
// backend, which does exactly this. If go-keyring is added to go.mod in a
// later phase, DarwinStore can be swapped for it without changing the
// Store interface or any caller.
package keychain

import (
	"context"
	"errors"
)

// Service is the macOS Keychain "service" name every Postbode secret is
// stored under (the -s argument to `security`). Account (the -a argument)
// distinguishes which secret within that service — see AccountClearFactsPAT
// and AccountGmailToken.
const Service = "be.vhco.postbode"

// Account names the two secrets Postbode ever stores (F-62).
const (
	AccountClearFactsPAT = "clearfacts-pat"
	AccountGmailToken    = "gmail-oauth-token"
)

// Store is a generic-password secret store keyed by account name within
// Service. Get returns ("", ErrNotFound) when no such secret exists yet —
// a normal, expected condition (first run, not yet authenticated), never
// logged as an error by callers.
type Store interface {
	Get(ctx context.Context, account string) (string, error)
	Set(ctx context.Context, account, value string) error
}

// ErrNotFound is returned by Store.Get when no secret is stored under the
// requested account.
var ErrNotFound = errors.New("keychain: secret not found")
