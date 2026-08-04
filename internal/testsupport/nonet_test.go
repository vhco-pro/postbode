package testsupport_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/testsupport"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "Network-isolation harness: a TestMain guard installing a dialer that panics on any dial to a non-loopback address, plus a make test-nonet target. The spec asserts \"outbound network blocked\" without specifying a mechanism (OQ-P9) (NF-09, AC-22)"
func TestGuardedDialContextPanicsOnNonLoopbackDial(t *testing.T) {
	called := false
	next := func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, nil
	}
	guarded := testsupport.GuardedDialContext(next)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("GuardedDialContext did not panic on a non-loopback dial attempt")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "api.clearfacts.example") {
			t.Errorf("panic value = %v, want a message naming the attempted address", r)
		}
		if called {
			t.Error("the wrapped dialer was invoked despite the address being non-loopback — the guard must panic BEFORE dialing, never after")
		}
	}()

	// api.clearfacts.be and gmail.googleapis.com are real hosts this test
	// must never actually resolve or contact — a synthetic, obviously
	// non-loopback address proves the same code path without touching the
	// network even by accident (NF-09 applies to this test too).
	_, _ = guarded(context.Background(), "tcp", "api.clearfacts.example:443")
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "Network-isolation harness: a TestMain guard installing a dialer that panics on any dial to a non-loopback address ... (NF-09, AC-22)"
func TestGuardedDialContextAllowsLoopbackDial(t *testing.T) {
	called := false
	next := func(ctx context.Context, network, addr string) (net.Conn, error) {
		called = true
		return nil, nil
	}
	guarded := testsupport.GuardedDialContext(next)

	for _, addr := range []string{"127.0.0.1:8080", "localhost:9090", "[::1]:1234"} {
		called = false
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("GuardedDialContext(%q) panicked: %v, want no panic for a loopback address", addr, r)
				}
			}()
			_, _ = guarded(context.Background(), "tcp", addr)
		}()
		if !called {
			t.Errorf("GuardedDialContext(%q) did not invoke the wrapped dialer for a loopback address", addr)
		}
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 11, Criterion: "AC-22: make test && go vet ./... passes, and a test-suite run with outbound network to api.clearfacts.be and gmail.googleapis.com blocked still passes 100%."
func TestInstallNetworkGuardIsNoOpWithoutTheEnvVar(t *testing.T) {
	t.Setenv(testsupport.NoNetworkEnvVar, "")
	before := http.DefaultTransport
	testsupport.InstallNetworkGuard()
	if http.DefaultTransport != before {
		t.Error("InstallNetworkGuard changed http.DefaultTransport despite POSTBODE_TEST_NO_NETWORK not being \"1\"")
	}
}
