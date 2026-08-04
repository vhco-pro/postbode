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

	// DocumentFunc, when set, backs the document(id:) read (F-05, F-37),
	// which ClearFacts serves as a plain application/json POST rather than
	// the multipart uploadFile contract this fake otherwise exists to
	// mimic. Called with the requested id; return (nil, nil) to have the
	// fake respond with a resolving document that echoes id back as
	// file.uuid (the default when DocumentFunc is nil too) — the common
	// case for proof-of-delivery tests (F-37, AC-16) that only care that
	// verification resolves. Return a non-nil error to simulate a
	// non-resolving/erroring document(id:) call.
	//
	// document(id:) calls are intentionally NOT appended to requests/
	// RequestCount: those exist specifically to assert on uploadFile calls
	// (AC-15's "exactly one multipart request"), and a verification call
	// following a successful upload must never silently inflate that count.
	DocumentFunc func(id string) (*ScriptedResponse, error)
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
	if isJSONRequest(r) {
		s.handleDocumentQuery(w, r)
		return
	}

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

// isJSONRequest reports whether r's Content-Type is application/json — the
// shape doJSON sends for administrations/document/companyStatistics, as
// opposed to uploadFile's multipart/form-data.
func isJSONRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

// handleDocumentQuery backs document(id:) reads (F-05, F-37). See
// Server.DocumentFunc's doc comment for the default/scriptable behaviour.
func (s *Server) handleDocumentQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, fmt.Sprintf("fake: decode document query: %v", err), http.StatusBadRequest)
		return
	}
	id, _ := body.Variables["id"].(string)

	s.mu.Lock()
	fn := s.DocumentFunc
	s.mu.Unlock()

	if fn != nil {
		resp, err := fn(id)
		if err != nil {
			writeScripted(w, ScriptedResponse{
				StatusCode:  http.StatusOK,
				ContentType: "application/json",
				Body:        fmt.Sprintf(`{"data":{"document":null},"errors":[{"message":%q}]}`, err.Error()),
			})
			return
		}
		if resp != nil {
			writeScripted(w, *resp)
			return
		}
	}

	payload := map[string]any{
		"data": map[string]any{
			"document": map[string]any{
				"date":         "2026-01-01",
				"comment":      "",
				"type":         "PURCHASE",
				"paymentState": "UNPAID",
				"file": map[string]any{
					"uuid":          id,
					"name":          "",
					"amountOfPages": 1,
					"comment":       "",
					"tags":          []string{},
				},
			},
		},
	}
	b, _ := json.Marshal(payload)
	writeScripted(w, ScriptedResponse{StatusCode: http.StatusOK, ContentType: "application/json", Body: string(b)})
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
