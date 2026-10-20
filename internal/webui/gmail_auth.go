package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const authCookieName = "postbode_gmail_consent"

// GmailAuth completes desktop OAuth consent without replacing the running
// service. Install must persist the token before publishing it to that service.
// Configure this before starting the HTTP server.
type GmailAuth struct {
	Config     oauth2.Config
	Install    func(context.Context, *oauth2.Token) error
	mu         sync.Mutex
	attempt    consentAttempt
	generation uint64
}

type consentAttempt struct {
	state      string
	verifier   string
	expires    time.Time
	generation uint64
}

func (s *Server) handleGmailAuthStart(w http.ResponseWriter, r *http.Request) {
	a := s.GmailAuth
	if a == nil || a.Install == nil {
		http.Error(w, "Gmail reconnect is unavailable. Check the daemon's credentials configuration.", http.StatusServiceUnavailable)
		return
	}
	var stateBytes [32]byte
	if _, err := rand.Read(stateBytes[:]); err != nil {
		http.Error(w, "Could not start Gmail reconnect. Please retry.", http.StatusInternalServerError)
		return
	}
	attempt := consentAttempt{
		state:    hex.EncodeToString(stateBytes[:]),
		verifier: oauth2.GenerateVerifier(),
		expires:  time.Now().Add(10 * time.Minute),
	}
	a.mu.Lock()
	a.generation++
	attempt.generation = a.generation
	a.attempt = attempt
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: authCookieName, Value: attempt.state, Path: "/auth/gmail/callback",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, a.Config.AuthCodeURL(attempt.state,
		oauth2.AccessTypeOffline, oauth2.ApprovalForce,
		oauth2.S256ChallengeOption(attempt.verifier)), http.StatusSeeOther)
}

func (s *Server) handleGmailAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	a := s.GmailAuth
	if a == nil || a.Install == nil {
		s.authFailure(w, http.StatusServiceUnavailable)
		return
	}
	cookie, err := r.Cookie(authCookieName)
	state := r.URL.Query().Get("state")
	a.mu.Lock()
	attempt := a.attempt
	valid := err == nil && tokensEqual(attempt.state, state) &&
		tokensEqual(attempt.state, cookie.Value) && time.Now().Before(attempt.expires)
	if valid {
		// Consume before making any network call, including for denial/error.
		a.attempt = consentAttempt{}
	}
	a.mu.Unlock()
	if !valid {
		s.authFailure(w, http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: authCookieName, Path: "/auth/gmail/callback", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	code := r.URL.Query().Get("code")
	if r.URL.Query().Get("error") != "" || code == "" {
		s.authFailure(w, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tok, err := a.Config.Exchange(ctx, code, oauth2.VerifierOption(attempt.verifier))
	if err != nil || tok.RefreshToken == "" {
		s.authFailure(w, http.StatusBadGateway)
		return
	}
	// A newer consent may have started while this exchange was in flight.
	// Serialize the final save and source replacement with new starts and
	// other installs so persisted and live credentials cannot diverge.
	a.mu.Lock()
	if attempt.generation != a.generation {
		a.mu.Unlock()
		s.authFailure(w, http.StatusConflict)
		return
	}
	err = a.Install(ctx, tok)
	a.mu.Unlock()
	if err != nil {
		s.authFailure(w, http.StatusInternalServerError)
		return
	}
	// The Strict review cookie may be absent on Google's cross-site redirect.
	// Successful, browser-bound consent establishes the local session again.
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: s.token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/?gmail=reconnected", http.StatusSeeOther)
}

func (s *Server) authFailure(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// Never expose provider responses, authorization codes or Keychain errors.
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8">
<title>Reconnect Gmail — Postbode</title><h1>Gmail was not reconnected</h1>
<p>The request may have expired, been declined, or could not be saved.</p>
<p><a href="/">Return to Postbode and choose Reconnect Gmail to retry</a>.</p>
<p>If your session expired, run <code>postbode review</code>.</p></html>`))
}
