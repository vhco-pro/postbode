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

var pageTemplate = template.Must(template.New("").ParseFS(templateFS, "templates/*.html"))

// Server is the embedded Postbode review UI (F-42, F-43): a plain
// html/template + embed.FS server over internal/queue, with every
// mutating route gated by a random per-daemon-start session token (F-46).
type Server struct {
	db    *queue.DB
	token string
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
		if !tokensEqual(s.token, r.FormValue("t")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
