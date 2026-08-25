package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
)

// runRetry implements `postbode retry <id>` and `postbode retry --all`
// (F-77). Exit codes follow spec §6.1 exactly:
//
//	0  the message(s) were unparked, or --all found nothing to do
//	1  the id is unknown or not parked, or the database could not be reached
//	2  usage: neither an id nor --all, or both, or more than one id
//
// The 2-vs-1 split matters: 2 means "you typed something wrong and nothing
// happened", 1 means "the command was well formed and could not be carried
// out". A monitoring script can tell those apart; a single non-zero cannot.
func runRetry(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("retry", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "reprocess every parked message")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	rest := fs.Args()
	if len(rest) > 1 {
		_, _ = fmt.Fprintf(stderr, "postbode: retry: expected at most one message id, got %d\n%s\n", len(rest), cli.RetryUsage)
		return 2
	}
	var id string
	if len(rest) == 1 {
		id = rest[0]
	}

	home, err := userHomeDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "postbode: retry: %v\n", err)
		return 1
	}

	ctx := context.Background()
	db, err := cli.OpenDB(ctx, cli.DefaultDBPath(home))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "postbode: retry: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	if err := cli.Retry(ctx, db, stdout, id, *all, time.Now().UTC()); err != nil {
		if errors.Is(err, cli.ErrRetryUsage) {
			_, _ = fmt.Fprintf(stderr, "postbode: %v\n%s\n", err, cli.RetryUsage)
			return 2
		}
		_, _ = fmt.Fprintf(stderr, "postbode: %v\n", err)
		return 1
	}
	return 0
}
