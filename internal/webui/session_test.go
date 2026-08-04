// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "**Session token (F-46, ratified v1.3)**: a random per-daemon-start token required on **every mutating request**"
package webui_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

// The token rotates every daemon start, so requiring it in the URL made
// every bookmark and open tab die with a bare "unauthorized". A first
// authenticated visit now sets a cookie so the plain loopback URL keeps
// working for that browser.
func TestTokenInURLSetsACookieSoThePlainURLKeepsWorking(t *testing.T) {
	ts, _, _ := newTestServer(t)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}

	resp, err := client.Get(ts.URL + "/?t=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tokenized visit = %d, want 200", resp.StatusCode)
	}

	resp2, err := client.Get(ts.URL + "/") // no token parameter at all
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("bare URL after a tokenized visit = %d, want 200 — the cookie should carry the session", resp2.StatusCode)
	}
}

// The cookie is a convenience, never a bypass.
func TestNoCredentialsIsUnauthorizedAndSaysHowToRecover(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp, err := (&http.Client{}).Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials = %d, want 401", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "postbode review") {
		t.Errorf("401 page does not tell the user how to recover; body=%q", string(b))
	}
}

func TestForgedCookieIsRejected(t *testing.T) {
	ts, _, _ := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: "postbode_session", Value: "not-the-token"})
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged cookie = %d, want 401", resp.StatusCode)
	}
}
