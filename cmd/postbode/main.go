// Command postbode is the single static binary for the Postbode invoice agent.
//
// Subcommands (F-60): postboded (daemon), review, status, log. This file owns
// dispatch and usage only; each subcommand's behaviour lands in its own phase.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `postbode — Gmail to ClearFacts purchase-invoice agent

usage: postbode <command> [flags]

commands:
  daemon    run the watcher/uploader loop (also installed as postboded)
  review    open the local review UI at 127.0.0.1:7391
  status    print queue counts, last poll, last upload uuid, stuck items
  log       print the local decision and upload log
`

// run is separated from main so tests can drive it without exiting the process.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "daemon", "review", "status", "log":
		// Implemented in later phases; dispatch exists now so the skeleton
		// builds, vets and tests clean from Phase 1 onward.
		fmt.Fprintf(stderr, "postbode: %q is not implemented yet\n", args[0])
		return 1
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "postbode: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
