package gmailwatch

import (
	"context"
	"net/http"
	"sync"

	"golang.org/x/oauth2"
)

// ReplaceableTokenSource lets browser consent repair the existing Gmail client
// while polling or label application is using it. A replacement is published
// only after the caller has persisted the new token.
type ReplaceableTokenSource struct {
	mu     sync.RWMutex
	ctx    context.Context
	cfg    *oauth2.Config
	source oauth2.TokenSource
}

func NewReplaceableTokenSource(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) *ReplaceableTokenSource {
	return &ReplaceableTokenSource{ctx: ctx, cfg: cfg, source: cfg.TokenSource(ctx, tok)}
}

// Client reads this source on every request. oauth2.NewClient would add an
// outer token cache that could keep an unexpired token after Replace.
func (s *ReplaceableTokenSource) Client() *http.Client {
	return &http.Client{Transport: &oauth2.Transport{Source: s}}
}

func (s *ReplaceableTokenSource) Token() (*oauth2.Token, error) {
	s.mu.RLock()
	source := s.source
	s.mu.RUnlock()
	// Refresh can perform network I/O. Do not let an old request block the
	// browser from publishing a repaired source; the next request uses it.
	return source.Token()
}

func (s *ReplaceableTokenSource) Replace(tok *oauth2.Token) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.source = s.cfg.TokenSource(s.ctx, tok)
}
