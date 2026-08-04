package webui

import (
	"context"
	"embed"
	"html/template"
	"net"
	"net/http"

	"github.com/vhco-pro/postbode/internal/queue"
)

// DefaultAddr is the sole address the review UI ever binds to in
// production (F-42, NF-04): loopback only, fixed port 7391.
const DefaultAddr = "127.0.0.1:7391"

//go:embed templates/*.html
var templateFS embed.FS

// logoPNG is the VH mark, embedded so the binary stays self-contained (no
// asset directory to install alongside it, F-60's "single static binary").
//
//go:embed static/logo.png
var logoPNG []byte

var pageTemplate = template.Must(template.New("").ParseFS(templateFS, "templates/*.html"))

// Server is the embedded Postbode review UI (F-42, F-43): a plain
// html/template + embed.FS server over internal/queue, with every
// mutating route gated by a random per-daemon-start session token (F-46).
type Server struct {
	db    *queue.DB
	token string
	// OnApprove, when non-nil, is called after an item transitions to
	// approved. It exists so the daemon can start uploading immediately
	// instead of waiting for its next tick — see Daemon.Nudge. It must not
	// block: the handler calls it on the request path.
	OnApprove func()
}

// NewServer builds a Server bound to db and protected by token. token
// must be non-empty — an empty token would mean "no auth required", which
// F-46 forbids outright.
func NewServer(db *queue.DB, token string) (*Server, error) {
	if token == "" {
		return nil, errEmptyToken
	}
	return &Server{db: db, token: token}, nil
}

// Handler returns the server's http.Handler — used directly by httptest in
// this package's tests, and by ListenAndServe/Serve in production.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	// The logo is deliberately NOT token-gated. It carries no data, and
	// gating it would leave the 401 page — the one a user with a stale
	// session actually sees — showing a broken image.
	mux.HandleFunc("GET /logo.png", s.handleLogo)
	mux.HandleFunc("GET /favicon.ico", s.handleLogo)
	mux.HandleFunc("GET /{$}", s.requireToken(s.handleList))
	mux.HandleFunc("GET /preview/{id}", s.requireToken(s.handlePreview))
	mux.HandleFunc("POST /items/{id}/approve", s.requireToken(s.handleApprove))
	mux.HandleFunc("POST /items/{id}/reject", s.requireToken(s.handleReject))
	mux.HandleFunc("POST /items/{id}/already-in-portal", s.requireToken(s.handleAlreadyInPortal))
	mux.HandleFunc("POST /items/{id}/override-peppol", s.requireToken(s.handleOverridePeppol))
	mux.HandleFunc("POST /approve-all", s.requireToken(s.handleApproveAll))
	return mux
}

// Listen binds to addr and returns the listener. Production must always
// pass DefaultAddr (F-42); tests pass "127.0.0.1:0" for an ephemeral
// loopback port so the real 7391 port is never required to be free.
func Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// ListenAndServe binds to DefaultAddr and serves the review UI until ctx
// is cancelled or the listener errors.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := Listen(DefaultAddr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve serves the review UI on an already-bound listener, stopping when
// ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	httpSrv := &http.Server{Handler: s.Handler()}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Close()
	}()
	return httpSrv.Serve(ln)
}

// requireToken wraps next so it only runs when the request carries a
// valid session token (F-46, AC-21): the token form field/query parameter
// "t", compared in constant time. Every route but /healthz is wrapped.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Accept the token from the "t" parameter OR from the session
		// cookie set on a previous authenticated visit.
		//
		// The cookie exists so the UI is reachable at a plain, bookmarkable
		// http://127.0.0.1:7391/. Without it the token had to be in the URL
		// on every visit, and since it rotates on each daemon start, every
		// bookmark and open tab turned into a bare "unauthorized" with no
		// hint about what to do — which is exactly how it was reported.
		// Same-origin, loopback-only, HttpOnly, SameSite=Strict; it carries
		// the same secret the URL did, so it widens nothing.
		if tokensEqual(s.token, r.FormValue("t")) {
			http.SetCookie(w, &http.Cookie{
				Name:     sessionCookieName,
				Value:    s.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			next(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookieName); err == nil && tokensEqual(s.token, c.Value) {
			next(w, r)
			return
		}

		s.writeUnauthorized(w)
	}
}

// sessionCookieName holds the session token after a first authenticated
// visit, so the bare loopback URL keeps working for that browser.
const sessionCookieName = "postbode_session"

// writeUnauthorized answers a missing/stale token with instructions rather
// than the word "unauthorized".
//
// A bare 401 is a dead end here: the token rotates on every daemon start,
// so the overwhelmingly likely cause is a restarted daemon and a stale
// bookmark — not an intruder. Telling the legitimate user the one command
// that fixes it costs nothing, because anyone who can reach this port can
// already read the token file as the same user.
func (s *Server) writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`<!doctype html>
<html><head><meta charset="utf-8"><title>Postbode — session expired</title>
<link rel="icon" type="image/png" href="/logo.png">
<style>
body{font:16px/1.6 -apple-system,system-ui,sans-serif;max-width:34rem;margin:0 auto;padding:5rem 1.5rem;
  color:#eaf0ff;min-height:100vh;
  background:linear-gradient(160deg,#020814 0%,#061229 48%,#0b1f45 100%);background-attachment:fixed}
img.mark{width:2.6rem;height:2.6rem;display:block;margin-bottom:1rem}
h1{font-size:1.5rem;letter-spacing:-.02em;margin:0 0 .5rem}
code{background:rgba(0,0,0,.3);padding:.15rem .45rem;border-radius:5px;font-size:.95em}
p{margin:.9rem 0;color:#9fb2d8}
</style></head><body>
<img class="mark" src="/logo.png" alt="">
<h1>Session expired</h1>
<p>Postbode's review UI uses a session token that changes every time the daemon
restarts, so bookmarks and old tabs stop working.</p>
<p>Open a fresh one from your terminal:</p>
<p><code>postbode review</code></p>
<p>That reads the current token and opens this page again. After that, plain
<code>http://127.0.0.1:7391/</code> keeps working in this browser until the next restart.</p>
</body></html>`))
}

// handleLogo serves the embedded VH mark for both /logo.png and
// /favicon.ico. Browsers accept a PNG for either, so one asset covers both
// rather than shipping a separate .ico.
func (s *Server) handleLogo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(logoPNG)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
