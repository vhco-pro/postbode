package gmailwatch_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/gmailwatch/fake"
	"github.com/vhco-pro/postbode/internal/notify"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 7, Criterion: "AC-20: With the fake OAuth server returning `invalid_grant`, the daemon stays alive, emits a notification containing a re-auth URL, leaves all queue rows untouched, and `postbode status` reports `re-auth needed` with token age. Polling resumes without restart once auth succeeds."
func TestPollSurvivesInvalidGrantAndResumesWithoutRestart(t *testing.T) {
	ctx := context.Background()

	tokenSrv := fake.NewInvalidGrantTokenServer()
	defer tokenSrv.Close()

	oauthCfg := &oauth2.Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		Endpoint:     oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/auth", TokenURL: tokenSrv.URL},
	}
	expired := &oauth2.Token{
		AccessToken:  "expired-access-token",
		RefreshToken: "expired-refresh-token",
		Expiry:       time.Now().Add(-time.Hour), // forces a refresh attempt on the very first call
	}

	gmailSrv := fake.NewServer()
	defer gmailSrv.Close()

	failingHTTPClient := oauthCfg.Client(ctx, expired)
	svc, err := gmail.NewService(ctx,
		option.WithHTTPClient(failingHTTPClient),
		option.WithEndpoint(gmailSrv.URL),
	)
	if err != nil {
		t.Fatalf("gmail.NewService: %v", err)
	}

	db := openTestDB(t)
	w := newTestWatcher(t, svc, db)
	w.OAuthConfig = oauthCfg
	fk := &notify.Fake{}
	w.Notifier = fk

	res, err := w.Poll(ctx)
	if err != nil {
		// F-16: the daemon must stay alive — a re-auth condition is never a
		// fatal Poll error.
		t.Fatalf("Poll returned a fatal error on invalid_grant, want the daemon to stay alive: %v", err)
	}
	if !res.ReauthNeeded {
		t.Error("PollResult.ReauthNeeded = false, want true")
	}

	if fk.Count() != 1 {
		t.Fatalf("notifier received %d message(s), want exactly 1", fk.Count())
	}
	msg := fk.All()[0]
	if !strings.Contains(msg, "http") {
		t.Errorf("re-auth notification %q does not contain a URL", msg)
	}

	st, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if st.LastAuthError == "" {
		t.Error("sync_state.LastAuthError is empty, want the invalid_grant condition recorded (F-17: re-auth needed flag)")
	}

	// "leaves all queue rows untouched": nothing was ever extracted or
	// staged, since the auth failure happens before any Gmail call
	// succeeds.
	items, err := db.ItemsByMessageID(ctx, "msg-any")
	if err != nil {
		t.Fatalf("ItemsByMessageID: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("items after a reauth-blocked poll = %d, want 0", len(items))
	}

	// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-38: ... no message_failure row is created for the in-flight message, and no message is parked."
	//
	// A re-auth condition is not a property of any one message: every
	// message would fail identically, so charging one of them would park
	// perfectly healthy mail the moment a token expired.
	parkedAfterReauth, err := db.ListParkedMessages(ctx)
	if err != nil {
		t.Fatalf("ListParkedMessages: %v", err)
	}
	if len(parkedAfterReauth) != 0 {
		t.Errorf("a re-auth condition parked %d message(s), want 0", len(parkedAfterReauth))
	}
	failedAfterReauth, err := db.FailedIDs(ctx)
	if err != nil {
		t.Fatalf("FailedIDs: %v", err)
	}
	if len(failedAfterReauth) != 0 {
		t.Errorf("a re-auth condition charged %d message(s) against their budget, want 0", len(failedAfterReauth))
	}

	// "Polling resumes without restart once auth succeeds": swap in a
	// working (unauthenticated fake, matching how labels_test.go builds
	// its service) client on the SAME Watcher — no restart, no rebuild —
	// and confirm the next poll succeeds normally.
	workingSvc, err := gmail.NewService(ctx,
		option.WithEndpoint(gmailSrv.URL),
		option.WithHTTPClient(http.DefaultClient),
		option.WithoutAuthentication(),
	)
	if err != nil {
		t.Fatalf("gmail.NewService (working): %v", err)
	}
	gmailSrv.MessagesListFunc = func(q, pageToken string) (*gmail.ListMessagesResponse, error) {
		return &gmail.ListMessagesResponse{}, nil
	}
	gmailSrv.ProfileFunc = func() (*gmail.Profile, error) {
		return &gmail.Profile{HistoryId: 1}, nil
	}
	w.Service = workingSvc

	res2, err := w.Poll(ctx)
	if err != nil {
		t.Fatalf("second Poll (after auth recovers): %v", err)
	}
	if res2.ReauthNeeded {
		t.Error("second Poll (after auth recovers): ReauthNeeded = true, want false")
	}

	st2, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState (after recovery): %v", err)
	}
	if st2.LastAuthError != "" {
		t.Errorf("sync_state.LastAuthError after recovery = %q, want empty", st2.LastAuthError)
	}
}
