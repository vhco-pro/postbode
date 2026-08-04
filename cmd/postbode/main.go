// Command postbode is the single static binary for the Postbode invoice agent.
//
// Subcommands (F-60): postboded (daemon), review, status, log. This file owns
// dispatch, flag parsing and process wiring only; the testable logic behind
// review/status/log lives in internal/cli.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vhco-pro/postbode/internal/cli"
	"github.com/vhco-pro/postbode/internal/webui"
)

const usage = `postbode — Gmail to ClearFacts purchase-invoice agent

usage: postbode <command> [flags]

commands:
  daemon                  run the watcher/uploader loop (also installed as postboded)
  review                  open the local review UI at 127.0.0.1:7391 in the default browser
  status                  print queue counts, last poll, last upload uuid, token age, stuck items
  status --find <term>    "is this already uploaded?" one-line verdict (vendor/filename/subject/etc.)
  log [--since <dur>]     print the local decision and upload log (e.g. --since 24h)
`

// userHomeDir resolves the home directory used to locate the queue
// database and the review-UI session token file. It is a package variable
// rather than a direct os.UserHomeDir call so tests can inject a temp
// directory and never touch the real home directory.
var userHomeDir = os.UserHomeDir

// reviewLauncher opens the tokenized review URL in the default browser. It
// is a package variable so tests can inject a fake and assert no browser
// is ever launched.
var reviewLauncher cli.Launcher = cli.MacOpenLauncher{}

// run is separated from main so tests can drive it without exiting the process.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "daemon":
		return runDaemon(args[1:], stdout, stderr)
	case "review":
		return runReview(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "log":
		return runLog(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "postbode: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// runStatus implements `postbode status` and `postbode status --find
// <term>` (F-64, F-39). Exit code is non-zero if anything is stuck > 48h,
// so it is usable from a monitoring script (F-64).
func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	find := fs.String("find", "", "search items by vendor, filename, subject, invoice number or amount (F-39)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	home, err := userHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "postbode: status: %v\n", err)
		return 1
	}

	ctx := context.Background()
	db, err := cli.OpenDB(ctx, cli.DefaultDBPath(home))
	if err != nil {
		fmt.Fprintf(stderr, "postbode: status: %v\n", err)
		return 1
	}
	defer db.Close()

	if *find != "" {
		matches, err := cli.Find(ctx, db, *find)
		if err != nil {
			fmt.Fprintf(stderr, "postbode: status --find: %v\n", err)
			return 1
		}
		cli.FormatFind(stdout, *find, matches)
		return 0
	}

	report, err := cli.BuildStatusReport(ctx, db, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "postbode: status: %v\n", err)
		return 1
	}
	cli.FormatStatus(stdout, report)
	if len(report.Stuck) > 0 {
		return 1
	}
	return 0
}

// runLog implements `postbode log [--since <duration>]` (F-28, F-65).
func runLog(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.Duration("since", 0, "only show entries at or after this long ago, e.g. 24h (default: no filter)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	home, err := userHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "postbode: log: %v\n", err)
		return 1
	}

	ctx := context.Background()
	db, err := cli.OpenDB(ctx, cli.DefaultDBPath(home))
	if err != nil {
		fmt.Fprintf(stderr, "postbode: log: %v\n", err)
		return 1
	}
	defer db.Close()

	entries, err := cli.BuildLog(ctx, db, *since, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "postbode: log: %v\n", err)
		return 1
	}
	cli.FormatLog(stdout, entries)
	return 0
}

// runReview implements `postbode review` (F-46): read the daemon's session
// token and open the tokenized review URL in the default browser.
func runReview(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	home, err := userHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "postbode: review: %v\n", err)
		return 1
	}

	if err := cli.Review(webui.DefaultTokenPath(home), webui.DefaultAddr, reviewLauncher); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	return 0
}

// main dispatches to run, with one alias: when this binary is invoked (or
// symlinked/copied) as "postboded", the "daemon" subcommand is implied —
// F-60's "single static Go binary with subcommands: postboded (daemon),
// postbode review, postbode status, postbode log" names postboded as a
// direct entry point, not merely "postbode daemon" under another name.
func main() {
	args := os.Args[1:]
	if filepath.Base(os.Args[0]) == "postboded" {
		args = append([]string{"daemon"}, args...)
	}
	os.Exit(run(args, os.Stdout, os.Stderr))
}
