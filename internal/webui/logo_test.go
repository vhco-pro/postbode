// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 8, Criterion: "**Embedded single-page review UI** — Go `html/template` + `embed.FS`. **No JS build step, no framework, no CDN, zero client-side JS** (NF-01)"
package webui_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The logo must be reachable WITHOUT a session token. Gating it would leave
// the 401 page — the page a user with a stale session actually sees —
// showing a broken image, and it carries no data worth protecting.
func TestLogoIsServedWithoutASessionToken(t *testing.T) {
	ts, _, _ := newTestServer(t)

	for _, path := range []string{"/logo.png", "/favicon.ico"} {
		resp, err := (&http.Client{}).Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (must not require a token)", path, resp.StatusCode)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
			t.Errorf("GET %s Content-Type = %q, want image/png", path, ct)
		}
		if len(body) < 1000 {
			t.Errorf("GET %s returned %d bytes; the asset looks empty", path, len(body))
		}
		if !strings.HasPrefix(string(body[:8]), "\x89PNG") {
			t.Errorf("GET %s is not a PNG (bad magic bytes)", path)
		}
	}
}

// The queue page must reference the favicon, otherwise the browser tab
// stays blank.
func TestQueuePageDeclaresTheFavicon(t *testing.T) {
	ts, _, _ := newTestServer(t)

	resp, err := (&http.Client{}).Get(ts.URL + "/?t=" + testToken)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `rel="icon"`) {
		t.Error("queue page declares no favicon; the browser tab renders blank")
	}
}
