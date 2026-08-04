package webui_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/webui"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Spec §3.6, Criterion: "AC-21: `GET /` and every `POST` without a valid session token returns 401; the listener is bound to `127.0.0.1` (verified by asserting a connection to the host's LAN IP is refused). (F-42, F-46, NF-04)"
func TestListenerBindsToLoopbackOnly(t *testing.T) {
	db, _ := openTestDB(t)
	srv, err := webui.NewServer(db, testToken)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	// Use an ephemeral port rather than the real 7391 so this test never
	// collides with a running daemon or a parallel test run, but the host
	// half of the address is the real, production one: 127.0.0.1.
	ln, err := webui.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address is not a *net.TCPAddr: %v", ln.Addr())
	}
	if !tcpAddr.IP.IsLoopback() || tcpAddr.IP.String() != "127.0.0.1" {
		t.Fatalf("listener bound to %s, want 127.0.0.1", tcpAddr.IP)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, ln) }()

	// GET / without a token must 401 (belt-and-suspenders with the
	// handlers_test.go coverage, exercised here against the real listener
	// rather than httptest, per AC-21's wording).
	resp, err := http.Get("http://" + tcpAddr.String() + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	// AC-21's LAN-IP-refused check: only run when a non-loopback interface
	// actually exists, since it is otherwise environment-dependent
	// (spec v1.3, planner finding OQ-P13).
	lanIP, found := firstNonLoopbackIPv4(t)
	if !found {
		t.Skip("no non-loopback IPv4 interface on this host; LAN-refused check skipped per AC-21's environment-conditional wording")
	}
	dialAddr := net.JoinHostPort(lanIP, itoa(int64(tcpAddr.Port)))
	conn, err := net.DialTimeout("tcp", dialAddr, 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("connection to LAN address %s unexpectedly succeeded; listener must be loopback-only", dialAddr)
	}
}

// firstNonLoopbackIPv4 returns the first non-loopback IPv4 address found on
// any active interface, and whether one was found at all.
func firstNonLoopbackIPv4(t *testing.T) (string, bool) {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("InterfaceAddrs: %v", err)
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		return ip4.String(), true
	}
	return "", false
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "Review UI is a single embedded local web page (Go `html/template` + `embed.FS`, no JS build step, no framework), bound to `127.0.0.1:7391` only. (F-42)"
func TestDefaultAddrIsLoopbackPort7391(t *testing.T) {
	if webui.DefaultAddr != "127.0.0.1:7391" {
		t.Fatalf("DefaultAddr = %q, want %q", webui.DefaultAddr, "127.0.0.1:7391")
	}
}
