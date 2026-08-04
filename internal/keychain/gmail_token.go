package keychain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/oauth2"
)

// SaveGmailToken marshals tok as JSON (the same shape
// gmailwatch.SaveToken/LoadCachedToken use for their on-disk cache) and
// stores it under AccountGmailToken. This is F-62's migration of the
// Gmail refresh token off disk and into the Keychain — gmailwatch itself
// is untouched (it still knows only "a *oauth2.Token"; where that token
// is persisted is the daemon's concern, wired here).
func SaveGmailToken(ctx context.Context, store Store, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return fmt.Errorf("keychain: encode gmail token: %w", err)
	}
	if err := store.Set(ctx, AccountGmailToken, string(b)); err != nil {
		return fmt.Errorf("keychain: save gmail token: %w", err)
	}
	return nil
}

// LoadGmailToken reads back a token saved by SaveGmailToken. It returns
// ErrNotFound verbatim (not wrapped further) so callers can distinguish
// "never authenticated yet" from any other failure — the daemon's
// startup/poll-loop wiring treats the two very differently (F-16).
func LoadGmailToken(ctx context.Context, store Store) (*oauth2.Token, error) {
	v, err := store.Get(ctx, AccountGmailToken)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("keychain: load gmail token: %w", err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(v), &tok); err != nil {
		return nil, fmt.Errorf("keychain: decode gmail token: %w", err)
	}
	return &tok, nil
}
