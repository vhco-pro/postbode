package webui_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// recordMessageReceivedAt pre-records a message with an explicit
// internal_date so stageTestItem's own RecordMessageIfNew (which does
// nothing on conflict) leaves the date intact.
func recordMessageReceivedAt(t *testing.T, db *queue.DB, ctx context.Context, gmailMessageID string, received time.Time) {
	t.Helper()
	if _, err := db.RecordMessageIfNew(ctx, queue.Message{
		GmailMessageID: gmailMessageID,
		From:           "billing@vendor.example",
		Subject:        "Invoice",
		InternalDate:   received.UTC(),
	}); err != nil {
		t.Fatalf("RecordMessageIfNew(%s): %v", gmailMessageID, err)
	}
}

// The queue must show, per item, when the mail actually arrived — otherwise
// a reviewer facing a backlog cannot tell a fresh invoice from a months-old
// one. Old mail is additionally called out as stale, and the list reads
// oldest-received first.
func TestListShowsReceivedDateAgeAndStaleMarker(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	// The extra hour keeps each age off the exact day boundary, so the
	// elapsed test runtime can't floor it down to the previous day.
	old := time.Now().Add(-(30*24*time.Hour + time.Hour))
	fresh := time.Now().Add(-(2*time.Hour + time.Minute))

	recordMessageReceivedAt(t, db, ctx, "msg-old", old)
	recordMessageReceivedAt(t, db, ctx, "msg-fresh", fresh)
	// Staged newest-first, to prove the page does not simply inherit
	// staged_at order.
	stageTestItem(t, db, ctx, "msg-fresh", "billing@vendor.example", "Invoice", "", "")
	stageTestItem(t, db, ctx, "msg-old", "billing@vendor.example", "Invoice", "", "")

	body := getList(t, ts.URL)

	for _, want := range []string{
		old.Local().Format("2 Jan 2006 15:04"),
		fresh.Local().Format("2 Jan 2006 15:04"),
		"30 days ago",
		"2 hours ago",
		"staged ",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("list page does not contain %q", want)
		}
	}

	// Only the 30-day-old item is stale, so exactly one age badge carries
	// the marker class.
	if got := strings.Count(body, `class="age stale"`); got != 1 {
		t.Errorf("stale age badges = %d, want 1", got)
	}

	// Oldest received first, regardless of staging order.
	iOld := strings.Index(body, "30 days ago")
	iFresh := strings.Index(body, "2 hours ago")
	if iOld == -1 || iFresh == -1 || iOld > iFresh {
		t.Errorf("oldest mail is not listed first (old at %d, fresh at %d)", iOld, iFresh)
	}
}

// A message row that can't be read must not blank out or crash the row: the
// item still has to be reviewable, just with an honest "unknown" date.
func TestListRendersUnknownReceivedDateWhenMessageHasNone(t *testing.T) {
	ts, db, _ := newTestServer(t)
	ctx := context.Background()

	stageTestItem(t, db, ctx, "msg-no-date", "billing@vendor.example", "Invoice", "", "")

	body := getList(t, ts.URL)
	if !strings.Contains(body, "unknown") {
		t.Error("list page does not render an unknown received date")
	}
	if !strings.Contains(body, "2026-08-04-vendor-invoice.pdf") {
		t.Error("item disappeared from the list when its received date was unknown")
	}
}

func getList(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/?t=" + testToken)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
