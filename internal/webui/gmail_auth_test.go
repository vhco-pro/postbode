package webui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/keychain"
	"github.com/vhco-pro/postbode/internal/notify"
	"github.com/vhco-pro/postbode/internal/queue"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

func authTestServer(t *testing.T) *Server {
	t.Helper()
	db, err := queue.Open(context.Background(), filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewServer(db, "session-secret")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startConsent(t *testing.T, s *Server) (url.Values, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/auth/gmail/start", strings.NewReader("t=session-secret"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("start = %d: %s", w.Code, w.Body.String())
	}
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == authCookieName {
			return u.Query(), c
		}
	}
	t.Fatal("no consent cookie")
	return nil, nil
}

func consentCallback(s *Server, state, code string, cookie *http.Cookie) *httptest.ResponseRecorder {
	u := "/auth/gmail/callback?" + url.Values{"state": {state}, "code": {code}}.Encode()
	r := httptest.NewRequest(http.MethodGet, u, nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// N3–N5: recover the SAME client after invalid_grant, with the production
// Keychain serialization against a fake store and real HTTP consent handlers.
func TestConsentRepairsRunningGmailClient(t *testing.T) {
	s := authTestServer(t)
	var challenge string
	exchanges := 0
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Form.Get("grant_type") == "refresh_token" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		exchanges++
		if r.Form.Get("code") != "approved" || r.Form.Get("redirect_uri") != "http://127.0.0.1:7391/auth/gmail/callback" || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != challenge {
			t.Error("exchange did not carry expected code, redirect URI and PKCE verifier")
		}
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer tokenServer.Close()
	cfg := oauth2.Config{ClientID: "client", RedirectURL: "http://127.0.0.1:7391/auth/gmail/callback", Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/consent", TokenURL: tokenServer.URL, AuthStyle: oauth2.AuthStyleInParams}}
	tokens := gmailwatch.NewReplaceableTokenSource(context.Background(), &cfg, &oauth2.Token{AccessToken: "expired", RefreshToken: "bad-refresh", Expiry: time.Now().Add(-time.Hour)})
	gmailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new-access" {
			t.Error("request used old token")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/profile") {
			_, _ = w.Write([]byte(`{"historyId":"42"}`))
		} else {
			_, _ = w.Write([]byte(`{"messages":[]}`))
		}
	}))
	defer gmailServer.Close()
	svc, err := gmail.NewService(context.Background(), option.WithHTTPClient(oauth2.NewClient(context.Background(), tokens)), option.WithEndpoint(gmailServer.URL+"/"))
	if err != nil {
		t.Fatal(err)
	}
	n := &notify.Fake{}
	watcher := &gmailwatch.Watcher{Service: svc, DB: s.db, UserID: "me", Notifier: n}
	for i := 0; i < 2; i++ {
		result, err := watcher.Poll(context.Background())
		if err != nil || !result.ReauthNeeded {
			t.Fatalf("expired poll = %+v, %v", result, err)
		}
	}
	if n.Count() != 1 {
		t.Fatalf("repeated failure emitted %d notifications", n.Count())
	}
	store := keychain.NewFake()
	s.GmailAuth = &GmailAuth{Config: cfg, Install: func(ctx context.Context, tok *oauth2.Token) error {
		if err := keychain.SaveGmailToken(ctx, store, tok); err != nil {
			return err
		}
		tokens.Replace(tok)
		return nil
	}}
	page := httptest.NewRecorder()
	s.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/?t=session-secret", nil))
	if !strings.Contains(page.Body.String(), "Gmail needs re-authentication") || !strings.Contains(page.Body.String(), "Reconnect Gmail") {
		t.Fatal("empty review queue lost recovery action")
	}
	q, cookie := startConsent(t, s)
	challenge = q.Get("code_challenge")
	if challenge == "" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || cookie.SameSite != http.SameSiteLaxMode || !cookie.HttpOnly {
		t.Fatal("missing PKCE or browser binding")
	}
	w := consentCallback(s, q.Get("state"), "approved", cookie)
	if w.Code != http.StatusSeeOther || exchanges != 1 {
		t.Fatalf("callback = %d, exchanges=%d: %s", w.Code, exchanges, w.Body.String())
	}
	stored, err := keychain.LoadGmailToken(context.Background(), store)
	if err != nil || stored.RefreshToken != "new-refresh" {
		t.Fatalf("saved token = %+v, %v", stored, err)
	}
	result, err := watcher.Poll(context.Background())
	if err != nil || result.ReauthNeeded {
		t.Fatalf("recovered poll = %+v, %v", result, err)
	}
	st, err := s.db.GetSyncState(context.Background())
	if err != nil || st.LastAuthError != "" || st.HistoryID != "42" {
		t.Fatalf("recovered state = %+v, %v", st, err)
	}
	if consentCallback(s, q.Get("state"), "approved", cookie).Code != http.StatusBadRequest || exchanges != 1 {
		t.Fatal("callback replay accepted")
	}
}

// N4/N6: reject missing session, cross-browser callbacks, stale attempts and
// provider denial without consuming a valid attempt on an unrelated request.
func TestConsentRejectsUnboundExpiredAndSupersededRequests(t *testing.T) {
	s := authTestServer(t)
	installed := false
	s.GmailAuth = &GmailAuth{Config: oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/consent"}}, Install: func(context.Context, *oauth2.Token) error { installed = true; return nil }}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/gmail/start", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed start = %d", w.Code)
	}
	q, cookie := startConsent(t, s)
	if consentCallback(s, q.Get("state"), "code", nil).Code != http.StatusBadRequest {
		t.Fatal("missing browser binding accepted")
	}
	if consentCallback(s, "wrong", "code", cookie).Code != http.StatusBadRequest {
		t.Fatal("invalid state accepted")
	}
	newQ, newCookie := startConsent(t, s)
	if newQ.Get("state") == q.Get("state") {
		t.Fatal("state reused")
	}
	if consentCallback(s, q.Get("state"), "code", cookie).Code != http.StatusBadRequest {
		t.Fatal("superseded attempt accepted")
	}
	s.GmailAuth.attempt.expires = time.Now().Add(-time.Second)
	if consentCallback(s, newQ.Get("state"), "code", newCookie).Code != http.StatusBadRequest {
		t.Fatal("expired attempt accepted")
	}
	q, cookie = startConsent(t, s)
	r := httptest.NewRequest(http.MethodGet, "/auth/gmail/callback?state="+q.Get("state")+"&error=%3Cscript%3E", nil)
	r.AddCookie(cookie)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "<script>") || !strings.Contains(w.Body.String(), "retry") {
		t.Fatal("denial not recoverable or provider error reflected")
	}
	if installed || s.GmailAuth.attempt.state != "" {
		t.Fatal("denied consent installed a token or left a reusable attempt")
	}
	startConsent(t, s) // Retry remains available.
}

func TestConsentExchangeAndStorageFailuresAreRecoverable(t *testing.T) {
	for _, mode := range []string{"exchange", "storage", "missing-refresh"} {
		t.Run(mode, func(t *testing.T) {
			s := authTestServer(t)
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if mode == "exchange" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":"secret-provider-error"}`))
					return
				}
				if mode == "missing-refresh" {
					_, _ = w.Write([]byte(`{"access_token":"secret-access"}`))
					return
				}
				_, _ = w.Write([]byte(`{"access_token":"secret-access","refresh_token":"secret-refresh"}`))
			}))
			defer tokenServer.Close()
			installCalls := 0
			s.GmailAuth = &GmailAuth{Config: oauth2.Config{Endpoint: oauth2.Endpoint{AuthURL: "https://accounts.example/consent", TokenURL: tokenServer.URL, AuthStyle: oauth2.AuthStyleInParams}}, Install: func(context.Context, *oauth2.Token) error { installCalls++; return errors.New("secret-storage-error") }}
			q, cookie := startConsent(t, s)
			w := consentCallback(s, q.Get("state"), "code", cookie)
			if w.Code < 400 || strings.Contains(w.Body.String(), "secret-") || !strings.Contains(w.Body.String(), "retry") {
				t.Fatalf("unsafe failure: %d %s", w.Code, w.Body.String())
			}
			if (mode == "storage" && installCalls != 1) || (mode != "storage" && installCalls != 0) {
				t.Fatalf("install calls = %d", installCalls)
			}
			startConsent(t, s)
		})
	}
}
