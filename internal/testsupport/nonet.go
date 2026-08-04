// Package testsupport provides Postbode's Phase 11 NF-09 enforcement
// mechanism (AC-22, OQ-P9): an in-process HTTP dial guard that panics on
// any attempted dial to a non-loopback address, so a test suite run with
// "outbound network blocked" is a fact the process itself enforces rather
// than an assumption about the environment it happens to run in.
//
// # Why a dialer guard, not an OS-level firewall
//
// Go has no built-in network sandbox, and `go test` cannot be trusted to
// always run under an external one (a developer's laptop is not a CI
// sandbox). The spec (v1.3, OQ-P9) resolves this by mandating an in-process
// guard: install it on http.DefaultTransport, the transport every HTTP
// client in this codebase uses by default (internal/clearfacts.NewClient's
// default Doer, and every gmail.NewService(option.WithHTTPClient(...))
// call across the test suite passes http.DefaultClient) — see
// internal/testsupport/nonet_test.go for the proof it fires.
package testsupport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
)

// NoNetworkEnvVar is the environment variable make test-nonet sets to
// activate the guard (AC-22). Unset or any value other than "1" leaves
// http.DefaultTransport untouched — InstallNetworkGuard is always safe to
// call unconditionally from a TestMain.
const NoNetworkEnvVar = "POSTBODE_TEST_NO_NETWORK"

var installOnce sync.Once

// InstallNetworkGuard installs the AC-22 dial guard onto
// http.DefaultTransport (and points http.DefaultClient at it) when
// POSTBODE_TEST_NO_NETWORK=1 is set in the environment. It is a no-op
// otherwise, and idempotent — safe to call from multiple TestMain functions
// or multiple times within one.
//
// Every HTTP call in this codebase that has not been explicitly pointed at
// a local httptest fake (clearfacts.WithEndpoint, gmail's
// option.WithEndpoint) goes out through http.DefaultClient/
// http.DefaultTransport, so this single install point covers the
// gmail.googleapis.com and api.clearfacts.be surfaces NF-09 names by name.
// A test that DOES correctly point at a loopback fake is unaffected — only
// a genuine non-loopback dial attempt panics.
func InstallNetworkGuard() {
	if os.Getenv(NoNetworkEnvVar) != "1" {
		return
	}
	installOnce.Do(func() {
		base := &net.Dialer{}
		guarded := &http.Transport{
			Proxy:                 nil, // a proxy would itself be a non-loopback dial target worth catching, not silently honoured
			DialContext:           GuardedDialContext(base.DialContext),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90_000_000_000, // 90s, spelled in ns to avoid importing "time" for one constant
			TLSHandshakeTimeout:   10_000_000_000, // 10s
			ExpectContinueTimeout: 1_000_000_000,  // 1s
		}
		http.DefaultTransport = guarded
		http.DefaultClient = &http.Client{Transport: guarded}
	})
}

// GuardedDialContext wraps a DialContext dial function with the AC-22
// no-network guard: any dial whose target host is not a loopback address
// panics instead of proceeding. Exported directly (not only reachable via
// InstallNetworkGuard) so a test can prove the guard fires without needing
// POSTBODE_TEST_NO_NETWORK set and without risking a real dial escaping —
// the panic happens before next is ever called.
func GuardedDialContext(next func(ctx context.Context, network, addr string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if !isLoopbackAddr(addr) {
			panic(fmt.Sprintf(
				"testsupport: NF-09 violation: attempted dial to non-loopback address %q (network=%s) — outbound network is blocked under POSTBODE_TEST_NO_NETWORK=1; every test must use a local httptest fake",
				addr, network,
			))
		}
		return next(ctx, network, addr)
	}
}

// isLoopbackAddr reports whether addr (a "host:port" or bare host) names a
// loopback address: 127.0.0.0/8, ::1, or the literal hostname "localhost".
// A host that is not a literal IP (e.g. a real DNS name) is never
// considered loopback — resolving it would itself require a network call
// this guard exists to prevent.
func isLoopbackAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
