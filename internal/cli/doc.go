// Package cli implements the logic behind Postbode's CLI surface (F-60):
// `postbode status`, `postbode status --find <term>`, `postbode log` and
// `postbode review`. It exists so cmd/postbode/main.go can stay a thin
// flag-parsing/dispatch shell — every testable decision (what counts as
// "stuck", how a queue item's local record collapses to one of G-5's five
// verdicts, how the log is assembled) lives here instead, driven entirely
// through internal/queue's existing exported API.
//
// # Scope and a known gap
//
// This package deliberately never adds to internal/queue's public surface
// (a concurrent phase owns that package). Every report here is built by
// walking internal/queue.DB.ListByStatus across the eight known statuses
// and following each item's gmail_message_id back to its message and
// decision_log rows. That means `postbode log` can only surface
// decision_log entries for messages that produced at least one queue item;
// a message whose rules-engine decision was "denied" or "dropped" before
// any item existed is invisible to it, because no exported method lists
// every gmail_message_id or every decision_log row directly. Closing that
// gap cleanly needs a small additive method on internal/queue.DB (e.g.
// AllDecisions) and is left as a follow-up rather than done here.
//
// # F-65 — no message bodies, ever
// Every string this package renders is built from data internal/queue and
// internal/rules already treat as safe to log: filenames, subjects,
// statuses, uuids, decisions and canned reason strings. No message or
// attachment body ever passes through this package, and none of the
// formatting functions here accept one.
package cli
