package cli_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/queue"
)

func parkedAt(h int) time.Time { return time.Date(2026, 8, 25, h, 0, 0, 0, time.UTC) }

// park puts id into the parked state the way a real poll would.
func park(t *testing.T, db *queue.DB, id string) {
	t.Helper()
	if _, ok, err := db.RecordMessageFailure(context.Background(), id, "500 backend error", 1, parkedAt(1), 6*time.Hour); err != nil || !ok {
		t.Fatalf("park(%s): parked=%v err=%v", id, ok, err)
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-41: postbode retry <id> on a parked message exits 0, clears parked_at ... postbode retry --all unparks every parked message and reports the count."
func TestRetryUnparksOneMessage(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	park(t, db, "msg-a")

	var out bytes.Buffer
	if err := cli.Retry(ctx, db, &out, "msg-a", false, parkedAt(2)); err != nil {
		t.Fatalf("Retry: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "msg-a") {
		t.Errorf("output does not name the message: %q", got)
	}
	if !strings.Contains(got, "next poll") {
		t.Errorf("output does not say the change takes effect on the next poll (it must not imply instant action): %q", got)
	}

	due, err := db.DueRetries(ctx, parkedAt(2))
	if err != nil {
		t.Fatalf("DueRetries: %v", err)
	}
	if len(due) != 1 || due[0] != "msg-a" {
		t.Errorf("DueRetries = %v, want [msg-a]", due)
	}

	// History is deliberately preserved: a human saying "try again" is not
	// a claim the message was never broken (F-75/F-76 depend on it).
	f, err := db.GetMessageFailure(ctx, "msg-a")
	if err != nil {
		t.Fatalf("GetMessageFailure: %v", err)
	}
	if f.FailureCount == 0 || f.ParkCount == 0 {
		t.Errorf("retry reset the failure history (%+v); F-76's effective budget of 1 depends on it", f)
	}
}

func TestRetryAllUnparksEverything(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	park(t, db, "msg-a")
	park(t, db, "msg-b")

	var out bytes.Buffer
	if err := cli.Retry(ctx, db, &out, "", true, parkedAt(2)); err != nil {
		t.Fatalf("Retry --all: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "unparked 2 message(s)") {
		t.Errorf("output does not report the count: %q", got)
	}
	for _, id := range []string{"msg-a", "msg-b"} {
		if !strings.Contains(got, id) {
			t.Errorf("output does not name %s: %q", id, got)
		}
	}

	due, err := db.DueRetries(ctx, parkedAt(2))
	if err != nil {
		t.Fatalf("DueRetries: %v", err)
	}
	if len(due) != 2 {
		t.Errorf("DueRetries = %v, want both", due)
	}
}

func TestRetryAllWithNothingParkedIsNotAnError(t *testing.T) {
	var out bytes.Buffer
	if err := cli.Retry(context.Background(), openTestDB(t), &out, "", true, parkedAt(2)); err != nil {
		t.Fatalf("Retry --all with nothing parked: %v", err)
	}
	if !strings.Contains(out.String(), "no parked messages") {
		t.Errorf("output = %q, want it to say there was nothing to do", out.String())
	}
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-41: ... postbode retry with no argument exits 2 and changes nothing; postbode retry <unknown-id> exits non-zero naming the id."
func TestRetryUsageErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("neither id nor --all", func(t *testing.T) {
		db := openTestDB(t)
		park(t, db, "msg-a")
		var out bytes.Buffer
		err := cli.Retry(ctx, db, &out, "", false, parkedAt(2))
		if !errors.Is(err, cli.ErrRetryUsage) {
			t.Fatalf("err = %v, want ErrRetryUsage", err)
		}
		// "changes nothing": the parked message must not have become due.
		due, _ := db.DueRetries(ctx, parkedAt(2))
		if len(due) != 0 {
			t.Errorf("a usage error made %v due; it must change nothing", due)
		}
	})

	t.Run("both id and --all", func(t *testing.T) {
		var out bytes.Buffer
		if err := cli.Retry(ctx, openTestDB(t), &out, "msg-a", true, parkedAt(2)); !errors.Is(err, cli.ErrRetryUsage) {
			t.Fatalf("err = %v, want ErrRetryUsage — 'retry everything' must be explicit", err)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		var out bytes.Buffer
		err := cli.Retry(ctx, openTestDB(t), &out, "msg-nope", false, parkedAt(2))
		if err == nil {
			t.Fatal("Retry on an unknown id returned nil")
		}
		if !strings.Contains(err.Error(), "msg-nope") {
			t.Errorf("error %q does not name the id", err)
		}
	})

	t.Run("known but not parked", func(t *testing.T) {
		db := openTestDB(t)
		// One failure under a budget of 3: recorded, but not parked.
		if _, _, err := db.RecordMessageFailure(ctx, "msg-failing", "boom", 3, parkedAt(1), time.Hour); err != nil {
			t.Fatalf("RecordMessageFailure: %v", err)
		}
		var out bytes.Buffer
		err := cli.Retry(ctx, db, &out, "msg-failing", false, parkedAt(2))
		if err == nil {
			t.Fatal("Retry on a non-parked message returned nil")
		}
		if !strings.Contains(err.Error(), "not parked") {
			t.Errorf("error %q does not explain that the message is not parked", err)
		}
	})
}

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "AC-41: ... The write succeeds while a second connection holds the same database open."
//
// `postbode retry` is the first CLI verb that writes to the queue while the
// daemon may be running. WAL plus busy_timeout=5000 is what makes that safe;
// this asserts it rather than trusting it.
func TestRetryWritesWhileASecondConnectionIsOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")

	db, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	park(t, db, "msg-a")

	// A second handle on the same file, standing in for the running daemon.
	daemon, err := queue.Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer func() { _ = daemon.Close() }()
	if _, err := daemon.ListParkedMessages(ctx); err != nil {
		t.Fatalf("daemon-side read: %v", err)
	}

	var out bytes.Buffer
	if err := cli.Retry(ctx, db, &out, "msg-a", false, parkedAt(2)); err != nil {
		t.Fatalf("Retry while a second connection is open: %v", err)
	}

	due, err := daemon.DueRetries(ctx, parkedAt(2))
	if err != nil {
		t.Fatalf("daemon-side DueRetries: %v", err)
	}
	if len(due) != 1 {
		t.Errorf("the daemon's connection does not see the retry: %v", due)
	}
}
