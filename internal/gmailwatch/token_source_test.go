package gmailwatch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestConsentImmediatelyReplacesUnexpiredTokenInExistingClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	ctx := context.Background()
	source := NewReplaceableTokenSource(ctx, &oauth2.Config{}, &oauth2.Token{
		AccessToken: "old-access", Expiry: time.Now().Add(time.Hour),
	})
	client := source.Client()
	check := func(want string) {
		t.Helper()
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil || string(body) != want {
			t.Fatalf("request authorization = %q, error %v; want %q", body, err, want)
		}
	}
	check("Bearer old-access")
	source.Replace(&oauth2.Token{AccessToken: "new-access", Expiry: time.Now().Add(time.Hour)})
	check("Bearer new-access")
}

func TestConsentCanReplaceSourceDuringBlockedRefresh(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	defer close(release)
	cfg := &oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInParams}}
	source := NewReplaceableTokenSource(context.Background(), cfg, &oauth2.Token{RefreshToken: "bad"})
	refreshed := make(chan struct{})
	go func() { _, _ = source.Token(); close(refreshed) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh never reached fake server")
	}
	replaced := make(chan struct{})
	go func() {
		source.Replace(&oauth2.Token{AccessToken: "repaired", Expiry: time.Now().Add(time.Hour)})
		close(replaced)
	}()
	select {
	case <-replaced:
	case <-time.After(3 * time.Second):
		t.Fatal("old network request prevented recovery")
	}
	for i := 0; i < 20; i++ {
		tok, err := source.Token()
		if err != nil || tok.AccessToken != "repaired" {
			t.Fatalf("new request = %+v, %v", tok, err)
		}
	}
}
