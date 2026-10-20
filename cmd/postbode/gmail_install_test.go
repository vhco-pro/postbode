package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/gmailwatch"
	"github.com/vhco-pro/postbode/internal/keychain"
	"golang.org/x/oauth2"
)

type unavailableStore struct{ keychain.Store }

func (unavailableStore) Set(context.Context, string, string) error {
	return errors.New("Keychain unavailable")
}

// N5: production wiring must leave the old working token in use when saving
// fails, then publish the replacement only after successful persistence.
func TestInstallGmailTokenPersistsBeforeReplacingLiveSource(t *testing.T) {
	ctx := context.Background()
	old := &oauth2.Token{AccessToken: "old", RefreshToken: "old-refresh", Expiry: time.Now().Add(time.Hour)}
	newToken := &oauth2.Token{AccessToken: "new", RefreshToken: "new-refresh", Expiry: time.Now().Add(time.Hour)}
	source := gmailwatch.NewReplaceableTokenSource(ctx, &oauth2.Config{}, old)
	if err := installGmailToken(ctx, unavailableStore{}, source, newToken); err == nil {
		t.Fatal("storage error ignored")
	}
	tok, err := source.Token()
	if err != nil || tok.AccessToken != "old" {
		t.Fatalf("storage failure replaced source: %+v, %v", tok, err)
	}
	store := keychain.NewFake()
	if err := installGmailToken(ctx, store, source, newToken); err != nil {
		t.Fatal(err)
	}
	tok, err = source.Token()
	if err != nil || tok.AccessToken != "new" {
		t.Fatalf("saved token not installed: %+v, %v", tok, err)
	}
	saved, err := keychain.LoadGmailToken(ctx, store)
	if err != nil || saved.RefreshToken != "new-refresh" {
		t.Fatalf("token not persisted: %+v, %v", saved, err)
	}
}
