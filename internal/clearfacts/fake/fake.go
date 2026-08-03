// Package fake is an httptest-backed double for the ClearFacts uploadFile
// multipart/form-data contract (NF-09). Every later phase that needs to
// assert on an upload — the Phase 2 client tests, the Phase 9 uploader, the
// Phase 11 e2e pipeline — scripts this server instead of touching
// api.clearfacts.be, which no test may ever do.
package fake

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
)

// CapturedRequest is a snapshot of one request the fake received.
type CapturedRequest struct {
	// Query is the "query" multipart field (the GraphQL operation text).
	Query string
	// Variables is the decoded "variables" multipart field.
	Variables map[string]any
	// FileFieldName is the multipart form field name the file part arrived
	// under — expected to be "file".
	FileFieldName string
	// FileName is the file part's Content-Disposition filename.
	FileName string
	// FileContent is the file part's bytes.
	FileContent []byte
	// ContentType is the request's Content-Type header, boundary included.
	ContentType string
	// AuthHeader is the raw Authorization header value received.
	AuthHeader string
}

// ScriptedResponse describes exactly what the fake should return for one
// request. StatusCode is required; Body is optional — when empty, a
// sensible default body for that status is generated.
type ScriptedResponse struct {
	StatusCode  int
	Body        string
	ContentType string
}

// Server is a scriptable fake of the ClearFacts multipart upload contract.
type Server struct {
	srv *httptest.Server

	mu       sync.Mutex
	queue    []ScriptedResponse
	requests []CapturedRequest
	seq      int
}

// New starts a fake server. Callers must Close it (typically via defer).
func New() *Server {
	s := &Server{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// URL is the fake's base URL, suitable for clearfacts.WithEndpoint.
func (s *Server) URL() string { return s.srv.URL }

// Close shuts the fake server down.
func (s *Server) Close() { s.srv.Close() }

// Enqueue schedules the next response the fake returns, FIFO. Once the
// queue drains, requests fall back to a generated success response — this
// is what lets a test script "503, 503, 503" and then rely on the default
// 200 for the fourth attempt (AC-17).
func (s *Server) Enqueue(resp ScriptedResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, resp)
}

// EnqueueStatusSequence is a convenience for scripting a run of plain status
// codes with generated bodies, e.g. EnqueueStatusSequence(503, 503, 503).
func (s *Server) EnqueueStatusSequence(statuses ...int) {
	for _, st := range statuses {
		s.Enqueue(ScriptedResponse{StatusCode: st})
	}
}

// EnqueueMalformedGraphQLErrors schedules a 200 response whose "errors"
// field is not a JSON array of GraphQL error objects — exercising the
// client's decode-failure path rather than a well-formed GraphQL error
// payload.
func (s *Server) EnqueueMalformedGraphQLErrors() {
	s.Enqueue(ScriptedResponse{
		StatusCode:  http.StatusOK,
		Body:        `{"errors": "not-an-array"}`,
		ContentType: "application/json",
	})
}

// Requests returns every request captured so far, oldest first.
func (s *Server) Requests() []CapturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CapturedRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

// RequestCount returns how many requests the fake has received.
func (s *Server) RequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Reset clears captured requests and any queued responses.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
	s.queue = nil
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	captured, err := captureRequest(r)
	if err != nil {
		http.Error(w, fmt.Sprintf("fake: %v", err), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.requests = append(s.requests, captured)
	var next *ScriptedResponse
	if len(s.queue) > 0 {
		n := s.queue[0]
		s.queue = s.queue[1:]
		next = &n
	}
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	if next != nil {
		writeScripted(w, *next)
		return
	}
	writeDefaultSuccess(w, captured, seq)
}

func captureRequest(r *http.Request) (CapturedRequest, error) {
	cr := CapturedRequest{
		ContentType: r.Header.Get("Content-Type"),
		AuthHeader:  r.Header.Get("Authorization"),
	}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return CapturedRequest{}, fmt.Errorf("parse content-type: %w", err)
	}
	if mediaType != "multipart/form-data" {
		return CapturedRequest{}, fmt.Errorf("unsupported content-type %q, want multipart/form-data (the uploadFile contract, F-50)", mediaType)
	}

	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return CapturedRequest{}, fmt.Errorf("read multipart part: %w", err)
		}

		switch part.FormName() {
		case "query":
			b, _ := io.ReadAll(part)
			cr.Query = string(b)
		case "variables":
			b, _ := io.ReadAll(part)
			var vars map[string]any
			if err := json.Unmarshal(b, &vars); err != nil {
				return CapturedRequest{}, fmt.Errorf("decode variables part: %w", err)
			}
			cr.Variables = vars
		default:
			b, _ := io.ReadAll(part)
			cr.FileFieldName = part.FormName()
			cr.FileName = part.FileName()
			cr.FileContent = b
		}
	}
	return cr, nil
}

func writeScripted(w http.ResponseWriter, resp ScriptedResponse) {
	ct := resp.ContentType
	if ct == "" {
		ct = "application/json"
	}
	body := resp.Body
	if body == "" {
		body = defaultBody(resp.StatusCode)
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.WriteString(w, body)
}

func defaultBody(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return `{"errors":[{"message":"unauthorized","extensions":{"code":"UNAUTHENTICATED"}}]}`
	case status >= 500:
		// Real 5xx responses are frequently not JSON at all; the client
		// must classify on status code alone in that case.
		return "service unavailable"
	case status >= 400:
		return `{"errors":[{"message":"bad request"}]}`
	default:
		return `{"data":{}}`
	}
}

func writeDefaultSuccess(w http.ResponseWriter, req CapturedRequest, seq int) {
	name := req.FileName
	if name == "" {
		if fn, ok := req.Variables["filename"].(string); ok {
			name = fn
		}
	}
	file := map[string]any{
		"uuid":          fmt.Sprintf("fake-uuid-%04d", seq),
		"name":          name,
		"amountOfPages": 1,
		"comment":       req.Variables["comment"],
		"tags":          req.Variables["tags"],
	}
	payload := map[string]any{"data": map[string]any{"uploadFile": file}}
	b, _ := json.Marshal(payload)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
