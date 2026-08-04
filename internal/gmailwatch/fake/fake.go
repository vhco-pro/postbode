// Package fake provides an in-process, scriptable double of the slice of
// the Gmail REST surface Postbode's gmailwatch package uses (NF-09 — no
// test may contact gmail.googleapis.com). It also provides a fake OAuth
// token endpoint for exercising F-16's re-auth handling without touching a
// real Google endpoint.
package fake

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"

	"google.golang.org/api/gmail/v1"
)

// APIError is returned by a Server's scriptable *Func fields to control the
// HTTP status and body of a simulated Gmail API failure — most notably a
// 404 to simulate "historyId not found" (F-13's trigger).
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gmailwatch/fake: %d %s", e.Code, e.Message)
}

// ModifyCall records one users.messages.modify invocation. Tests assert on
// the accumulated slice to prove F-19 ("no Gmail state other than the
// VH&Co/submitted label is ever written") and F-14 (label move semantics).
type ModifyCall struct {
	MessageID string
	Request   *gmail.ModifyMessageRequest
}

// Server is a scriptable fake of the Gmail REST endpoints gmailwatch calls:
// users.labels.list, users.history.list, users.messages.list,
// users.messages.get, users.messages.modify and users.getProfile. Every
// *Func field is optional — a nil field yields a valid, empty zero-value
// response, so a test only sets the handlers it actually needs to script.
type Server struct {
	*httptest.Server

	mu sync.Mutex

	// Labels backs users.labels.list.
	Labels []*gmail.Label

	// HistoryFunc backs users.history.list, called once per page request.
	// Return an *APIError with Code 404 to simulate a history gap.
	HistoryFunc func(startHistoryID uint64, pageToken string) (*gmail.ListHistoryResponse, error)

	// MessagesListFunc backs users.messages.list (the F-13 fallback path).
	// q carries the exact query string gmailwatch sent, letting tests
	// assert on F-13's inbox-scoped, window-bounded query.
	MessagesListFunc func(q, pageToken string) (*gmail.ListMessagesResponse, error)

	// MessagesGetFunc backs users.messages.get.
	MessagesGetFunc func(id, format string) (*gmail.Message, error)

	// ProfileFunc backs users.getProfile.
	ProfileFunc func() (*gmail.Profile, error)

	// LastHistoryLabelID records the labelId query parameter of the most
	// recent users.history.list call — used by scope_test.go to prove F-11
	// (whole-INBOX watch scope).
	LastHistoryLabelID string
	// LastMessagesListQuery records the q query parameter of the most
	// recent users.messages.list call — used by fallback_query_test.go to
	// prove F-13's exact query shape.
	LastMessagesListQuery string

	// ModifyCalls accumulates every users.messages.modify invocation, in
	// order.
	ModifyCalls []ModifyCall
}

// NewServer starts a fake Gmail API server. Callers must Close it (embeds
// *httptest.Server, so Close is already promoted).
func NewServer() *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /gmail/v1/users/{userId}/labels", s.handleLabelsList)
	mux.HandleFunc("GET /gmail/v1/users/{userId}/history", s.handleHistoryList)
	mux.HandleFunc("GET /gmail/v1/users/{userId}/messages", s.handleMessagesList)
	mux.HandleFunc("GET /gmail/v1/users/{userId}/messages/{id}", s.handleMessagesGet)
	mux.HandleFunc("POST /gmail/v1/users/{userId}/messages/{id}/modify", s.handleMessagesModify)
	mux.HandleFunc("GET /gmail/v1/users/{userId}/profile", s.handleProfile)
	s.Server = httptest.NewServer(mux)
	return s
}

// Calls returns a snapshot of every recorded users.messages.modify call.
func (s *Server) Calls() []ModifyCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ModifyCall, len(s.ModifyCalls))
	copy(out, s.ModifyCalls)
	return out
}

func (s *Server) handleLabelsList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	labels := s.Labels
	s.mu.Unlock()
	writeJSON(w, &gmail.ListLabelsResponse{Labels: labels})
}

func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	startHistoryID, _ := strconv.ParseUint(q.Get("startHistoryId"), 10, 64)
	pageToken := q.Get("pageToken")

	s.mu.Lock()
	s.LastHistoryLabelID = q.Get("labelId")
	fn := s.HistoryFunc
	s.mu.Unlock()

	if fn == nil {
		writeJSON(w, &gmail.ListHistoryResponse{HistoryId: startHistoryID})
		return
	}
	resp, err := fn(startHistoryID, pageToken)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleMessagesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	pageToken := q.Get("pageToken")

	s.mu.Lock()
	s.LastMessagesListQuery = query
	fn := s.MessagesListFunc
	s.mu.Unlock()

	if fn == nil {
		writeJSON(w, &gmail.ListMessagesResponse{})
		return
	}
	resp, err := fn(query, pageToken)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleMessagesGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	format := r.URL.Query().Get("format")

	s.mu.Lock()
	fn := s.MessagesGetFunc
	s.mu.Unlock()

	if fn == nil {
		writeError(w, &APIError{Code: http.StatusNotFound, Message: "message not found (no MessagesGetFunc scripted)"})
		return
	}
	msg, err := fn(id, format)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, msg)
}

func (s *Server) handleMessagesModify(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req gmail.ModifyMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, &APIError{Code: http.StatusBadRequest, Message: "malformed modify request: " + err.Error()})
		return
	}
	s.mu.Lock()
	s.ModifyCalls = append(s.ModifyCalls, ModifyCall{MessageID: id, Request: &req})
	s.mu.Unlock()
	writeJSON(w, &gmail.Message{Id: id})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	fn := s.ProfileFunc
	s.mu.Unlock()

	if fn == nil {
		writeJSON(w, &gmail.Profile{})
		return
	}
	profile, err := fn()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, profile)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	msg := err.Error()
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		code = apiErr.Code
		msg = apiErr.Message
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": msg},
	})
}

// NewInvalidGrantTokenServer starts a fake OAuth token endpoint that
// responds to every request with RFC 6749's invalid_grant error — for
// exercising gmailwatch's F-16 re-auth handling (AC-20) without touching
// Google's real token endpoint.
func NewInvalidGrantTokenServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
}
