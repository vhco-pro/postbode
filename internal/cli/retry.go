package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/vhco-pro/postbode/internal/queue"
)

// ErrRetryUsage is returned for an invocation that names neither a message
// id nor --all, or names both. The caller maps it to exit code 2 (F-77):
// "retry everything" must be asked for explicitly, never reached by
// accident or by a typo that dropped an argument.
var ErrRetryUsage = fmt.Errorf("retry: specify a message id or --all")

// RetryUsage is the one-line usage text printed alongside ErrRetryUsage.
const RetryUsage = `usage: postbode retry <gmail-message-id>
       postbode retry --all`

// Retry implements `postbode retry` (F-77): it clears a parked message's
// cooldown so the daemon's next poll picks it up again.
//
// It communicates with the running daemon through SQLite rather than any
// IPC channel or lock file. The database is opened WAL with a 5s busy
// timeout, which is exactly what makes a small write from a second process
// safe while the daemon holds the same file open — so the change lands
// without stopping anything, and takes effect on the next poll tick. The
// output says so rather than implying the retry happens instantly.
//
// It deliberately does NOT reset failure_count, park_count or retry_count.
// Those are the history that keeps F-76's effective-budget-of-1 and F-75's
// attempt bound working: a human saying "try again" is not a claim that the
// message was never broken.
func Retry(ctx context.Context, db *queue.DB, out io.Writer, id string, all bool, now time.Time) error {
	if (id == "") == !all {
		// Neither, or both.
		return ErrRetryUsage
	}

	if all {
		parked, err := db.ListParkedMessages(ctx)
		if err != nil {
			return err
		}
		if len(parked) == 0 {
			_, _ = fmt.Fprintln(out, "retry: no parked messages")
			return nil
		}
		n, err := db.UnparkAll(ctx, now)
		if err != nil {
			return err
		}
		for _, f := range parked {
			printRetried(out, f)
		}
		_, _ = fmt.Fprintf(out, "retry: unparked %d message(s) — they will be reprocessed on the next poll\n", n)
		return nil
	}

	f, err := db.GetMessageFailure(ctx, id)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("retry: no such message %s (it has no recorded failures)", id)
	}
	if !f.Parked() {
		return fmt.Errorf("retry: message %s is not parked (it has %d recorded failure(s) but the poll is still retrying it on its own)", id, f.FailureCount)
	}

	ok, err := db.Unpark(ctx, id, now)
	if err != nil {
		return err
	}
	if !ok {
		// Lost a race with the daemon clearing it — a good outcome, but say
		// so rather than claiming to have done something.
		return fmt.Errorf("retry: message %s was no longer parked", id)
	}
	printRetried(out, *f)
	_, _ = fmt.Fprintln(out, "retry: it will be reprocessed on the next poll")
	return nil
}

func printRetried(out io.Writer, f queue.MessageFailure) {
	_, _ = fmt.Fprintf(out, "retry: %s (%d failure(s), last error: %s)\n",
		f.GmailMessageID, f.FailureCount, errOrNone(f.LastError))
}

func errOrNone(s string) string {
	if s == "" {
		return "none recorded"
	}
	return s
}
