// Package gmailwatch holds the Gmail-facing production logic Postbode's
// daemon will use from Phase 7 onward. cmd/spike (Phase 3) is a thin caller
// over this package, not a reimplementation, so that deleting cmd/spike in
// Phase 15 removes zero production logic (F-07, plan Phase 3 task 7).
//
// auth.go owns the OAuth desktop-app flow and token cache (F-10). labels.go
// owns label resolution (F-03, F-15).
package gmailwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Scopes are the two Gmail scopes Postbode ever requests: readonly for
// polling, modify solely to apply the submitted label (F-10).
var Scopes = []string{
	gmail.GmailReadonlyScope,
	gmail.GmailModifyScope,
}

// loopbackAuthTimeout bounds how long AuthenticateInteractive waits for the
// developer to complete the browser consent flow before giving up.
const loopbackAuthTimeout = 5 * time.Minute

// CredentialsFileExists reports whether a Google OAuth desktop-app
// credentials file exists at path. D-2 (the human prerequisite that
// produces this file) is not guaranteed to be satisfied yet — callers use
// this to decide whether the Gmail leg can run at all, rather than crashing
// on a missing file (spec Phase 3 task: "the spike must skip leg (d) with a
// clear SKIPPED message and still exit 0" when credentials.json is absent).
func CredentialsFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// LoadOAuthConfig reads a Google OAuth desktop-app credentials.json file
// (D-2) and returns the corresponding oauth2.Config, scoped to Scopes.
func LoadOAuthConfig(path string) (*oauth2.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gmailwatch: read credentials file %s: %w", path, err)
	}
	cfg, err := google.ConfigFromJSON(b, Scopes...)
	if err != nil {
		return nil, fmt.Errorf("gmailwatch: parse credentials file %s: %w", path, err)
	}
	return cfg, nil
}

// LoadCachedToken reads a previously saved token from path. Access token
// (never refresh token alone) freshness is the caller's concern via
// cfg.TokenSource, which refreshes transparently.
func LoadCachedToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gmailwatch: read token cache %s: %w", path, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("gmailwatch: decode token cache %s: %w", path, err)
	}
	return &tok, nil
}

// SaveToken writes tok to path at mode 0600 (F-10 — refresh token cached
// locally, never in the config file), creating the parent directory if
// needed.
func SaveToken(path string, tok *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("gmailwatch: create token cache dir: %w", err)
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("gmailwatch: encode token: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("gmailwatch: write token cache %s: %w", path, err)
	}
	return nil
}

// AuthenticateInteractive returns a valid token for cfg, reusing and
// refreshing a cached token at tokenCachePath when possible and otherwise
// running the OAuth desktop consent flow via a local loopback redirect
// listener (opening a browser is the developer's job — the URL is printed
// to stderr). The resulting token is cached at tokenCachePath (F-10).
//
// This function is inherently interactive/live and is exercised only by
// cmd/spike in practice (NF-09) — it is not unit-tested against a live
// Google endpoint. LoadOAuthConfig, LoadCachedToken and SaveToken, which it
// composes, are unit-tested.
func AuthenticateInteractive(ctx context.Context, cfg *oauth2.Config, tokenCachePath string) (*oauth2.Token, error) {
	if cached, err := LoadCachedToken(tokenCachePath); err == nil {
		ts := cfg.TokenSource(ctx, cached)
		refreshed, err := ts.Token()
		if err == nil {
			if refreshed.AccessToken != cached.AccessToken || refreshed.RefreshToken != cached.RefreshToken {
				if err := SaveToken(tokenCachePath, refreshed); err != nil {
					return nil, err
				}
			}
			return refreshed, nil
		}
		// Refresh failed (e.g. invalid_grant / expired per F-16) — fall
		// through to a fresh interactive consent flow rather than failing.
		_, _ = fmt.Fprintf(os.Stderr, "gmailwatch: cached token could not be refreshed (%v); re-authenticating\n", err)
	}

	code, redirectURL, err := receiveAuthCodeViaLoopback(cfg)
	if err != nil {
		return nil, err
	}
	cfg.RedirectURL = redirectURL

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("gmailwatch: exchange auth code: %w", err)
	}
	if err := SaveToken(tokenCachePath, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

// receiveAuthCodeViaLoopback starts a one-shot HTTP listener on
// 127.0.0.1:<random port>, prints the consent URL for the developer to open,
// and waits for Google's redirect carrying the authorization code.
func receiveAuthCodeViaLoopback(cfg *oauth2.Config) (code, redirectURL string, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("gmailwatch: listen for OAuth redirect: %w", err)
	}
	defer func() { _ = ln.Close() }()

	redirectURL = fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if e := r.URL.Query().Get("error"); e != "" {
			_, _ = fmt.Fprintf(w, "Postbode: authentication failed: %s. You can close this tab.", e)
			select {
			case errCh <- fmt.Errorf("gmailwatch: oauth consent error: %s", e):
			default:
			}
			return
		}
		if c := r.URL.Query().Get("code"); c != "" {
			_, _ = fmt.Fprint(w, "Postbode: authenticated. You can close this tab.")
			select {
			case codeCh <- c:
			default:
			}
			return
		}
		http.Error(w, "missing code", http.StatusBadRequest)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	cfgCopy := *cfg
	cfgCopy.RedirectURL = redirectURL
	authURL := cfgCopy.AuthCodeURL("postbode-spike", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	_, _ = fmt.Fprintf(os.Stderr, "gmailwatch: open this URL to authorize Gmail access:\n\n  %s\n\n", authURL)

	select {
	case c := <-codeCh:
		return c, redirectURL, nil
	case e := <-errCh:
		return "", "", e
	case <-time.After(loopbackAuthTimeout):
		return "", "", fmt.Errorf("gmailwatch: timed out waiting for OAuth consent")
	}
}

// NewService builds an authenticated *gmail.Service from cfg/tok. Extra
// ClientOptions (used only by tests, to point at a fake endpoint) are
// appended after the auth client option.
func NewService(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token, opts ...option.ClientOption) (*gmail.Service, error) {
	httpClient := cfg.Client(ctx, tok)
	allOpts := append([]option.ClientOption{option.WithHTTPClient(httpClient)}, opts...)
	svc, err := gmail.NewService(ctx, allOpts...)
	if err != nil {
		return nil, fmt.Errorf("gmailwatch: build gmail service: %w", err)
	}
	return svc, nil
}
