// Package clearfacts is a client for the ClearFacts GraphQL API
// (https://api.clearfacts.be/graphql). It covers every call Postbode makes:
// uploadFile (F-50, F-56), administrations (F-01), document (F-05, F-37) and
// companyStatistics (F-08). Every call is transport-tested against a fake in
// internal/clearfacts/fake or an inline httptest server — nothing here ever
// contacts api.clearfacts.be from a test (NF-09).
//
// The upload signature deliberately never sends the deprecated `vatnumber`
// argument — only `companyNumber` (A-12). See upload.go.
package clearfacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

// defaultEndpoint is the production ClearFacts GraphQL endpoint. Tests always
// override it via WithEndpoint to point at a local fake (NF-09).
const defaultEndpoint = "https://api.clearfacts.be/graphql"

const (
	defaultMaxConcurrent = 1               // F-54
	defaultMinInterval   = 2 * time.Second // F-54
)

// Doer is the subset of *http.Client the client needs. Small and explicit so
// tests can substitute anything, though in practice http.DefaultClient (or a
// client pointed at an httptest server via WithEndpoint) is what's used.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a ClearFacts GraphQL client. Construct with NewClient.
type Client struct {
	endpoint string
	doer     Doer
	tokens   TokenSource
	limiter  *rateLimiter
}

type config struct {
	endpoint      string
	doer          Doer
	maxConcurrent int
	minInterval   time.Duration
}

// Option configures a Client constructed by NewClient.
type Option func(*config)

// WithEndpoint overrides the GraphQL endpoint. Tests must always set this to
// a local httptest/fake URL (NF-09).
func WithEndpoint(url string) Option {
	return func(c *config) { c.endpoint = url }
}

// WithHTTPClient overrides the HTTP transport used to send requests.
func WithHTTPClient(d Doer) Option {
	return func(c *config) { c.doer = d }
}

// WithMaxConcurrent overrides the default max_concurrent rate-discipline
// setting (F-54). Default is 1.
func WithMaxConcurrent(n int) Option {
	return func(c *config) { c.maxConcurrent = n }
}

// WithMinInterval overrides the default min_interval rate-discipline setting
// (F-54). Default is 2s; tests should set this to 0 to run fast.
func WithMinInterval(d time.Duration) Option {
	return func(c *config) { c.minInterval = d }
}

// NewClient builds a ClearFacts client. tokens supplies the PAT for every
// request (F-55); see EnvTokenSource for the dev implementation.
func NewClient(tokens TokenSource, opts ...Option) *Client {
	cfg := config{
		endpoint:      defaultEndpoint,
		doer:          http.DefaultClient,
		maxConcurrent: defaultMaxConcurrent,
		minInterval:   defaultMinInterval,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Client{
		endpoint: cfg.endpoint,
		doer:     cfg.doer,
		tokens:   tokens,
		limiter:  newRateLimiter(cfg.maxConcurrent, cfg.minInterval),
	}
}

// GraphQLError is one entry of a GraphQL response's top-level "errors" array.
// ClearFacts, like any GraphQL server, can return these on an HTTP 200 — that
// is the classifier's job to catch (see classify.go).
type GraphQLError struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

type graphQLRequestBody struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type graphQLEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []GraphQLError  `json:"errors,omitempty"`
}

// Error is returned for any classified GraphQL/HTTP failure. Callers use
// IsRetryable / IsNotifyWorthy (or inspect Decision directly) to decide what
// to do next; persistence and scheduling of that decision is Phase 9's job.
type Error struct {
	Decision      Decision
	StatusCode    int
	GraphQLErrors []GraphQLError
	Body          string
}

func (e *Error) Error() string {
	return fmt.Sprintf("clearfacts: %s (status=%d, retryable=%t)", e.Decision.Reason, e.StatusCode, e.Decision.Retryable)
}

// IsRetryable reports whether err is a classified clearfacts.Error the caller
// should retry (F-51). Unclassified errors (transport-level construction
// failures, decode errors) are treated as non-retryable.
func IsRetryable(err error) bool {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Decision.Retryable
	}
	return false
}

// IsNotifyWorthy reports whether err represents a PAT problem the operator
// should be told about (F-51, NF-02) — e.g. a 401/403.
func IsNotifyWorthy(err error) bool {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Decision.NotifyWorthy
	}
	return false
}

// authorize attaches the redacted-in-logs, real-on-the-wire bearer token.
func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("clearfacts: obtain token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok.Reveal())
	return nil
}

// doJSON executes a non-upload GraphQL operation (administrations, document,
// companyStatistics) as a plain application/json POST.
func (c *Client) doJSON(ctx context.Context, query string, variables map[string]any) (json.RawMessage, error) {
	release, err := c.limiter.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("clearfacts: rate limiter: %w", err)
	}
	defer release()

	body, err := json.Marshal(graphQLRequestBody{Query: query, Variables: variables})
	if err != nil {
		return nil, fmt.Errorf("clearfacts: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("clearfacts: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, c.classifyTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()

	return decodeResponse(resp)
}

// doMultipart executes the uploadFile mutation as multipart/form-data with
// parts "query", "variables" and the binary under fileFieldName (F-50).
func (c *Client) doMultipart(ctx context.Context, query string, variables map[string]any, fileFieldName, fileName string, fileContent io.Reader) (json.RawMessage, error) {
	release, err := c.limiter.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("clearfacts: rate limiter: %w", err)
	}
	defer release()

	varsJSON, err := json.Marshal(variables)
	if err != nil {
		return nil, fmt.Errorf("clearfacts: encode variables: %w", err)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if err := mw.WriteField("query", query); err != nil {
		return nil, fmt.Errorf("clearfacts: write query part: %w", err)
	}
	if err := mw.WriteField("variables", string(varsJSON)); err != nil {
		return nil, fmt.Errorf("clearfacts: write variables part: %w", err)
	}
	fw, err := mw.CreateFormFile(fileFieldName, fileName)
	if err != nil {
		return nil, fmt.Errorf("clearfacts: create file part: %w", err)
	}
	if _, err := io.Copy(fw, fileContent); err != nil {
		return nil, fmt.Errorf("clearfacts: write file part: %w", err)
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("clearfacts: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &buf)
	if err != nil {
		return nil, fmt.Errorf("clearfacts: build request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	if err := c.authorize(ctx, req); err != nil {
		return nil, err
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		return nil, c.classifyTransportErr(err)
	}
	defer func() { _ = resp.Body.Close() }()

	return decodeResponse(resp)
}

func (c *Client) classifyTransportErr(err error) error {
	dec := Classify(ClassifyInput{TransportErr: err})
	return &Error{Decision: dec}
}

// decodeResponse classifies the HTTP status and, per F-51's GraphQL gotcha,
// the payload's "errors" array too — a 200 with errors is not a success.
func decodeResponse(resp *http.Response) (json.RawMessage, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("clearfacts: read response: %w", err)
	}

	var env graphQLEnvelope
	decodeErr := json.Unmarshal(raw, &env)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		dec := Classify(ClassifyInput{StatusCode: resp.StatusCode, GraphQLErrors: env.Errors})
		return nil, &Error{Decision: dec, StatusCode: resp.StatusCode, GraphQLErrors: env.Errors, Body: string(raw)}
	}

	if decodeErr != nil {
		return nil, fmt.Errorf("clearfacts: decode response: %w (body=%q)", decodeErr, truncate(raw, 200))
	}

	dec := Classify(ClassifyInput{StatusCode: resp.StatusCode, GraphQLErrors: env.Errors})
	if dec.Reason != "" {
		return nil, &Error{Decision: dec, StatusCode: resp.StatusCode, GraphQLErrors: env.Errors, Body: string(raw)}
	}
	return env.Data, nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
