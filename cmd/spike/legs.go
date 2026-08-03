// DELETE AFTER P1 — see main.go and plan Phase 3/15 (F-07).
package main

import "fmt"

// legStatus is the terminal state of one spike round-trip.
type legStatus int

const (
	legPassed legStatus = iota
	legFailed
	legSkipped
)

func (s legStatus) String() string {
	switch s {
	case legPassed:
		return "PASS"
	case legFailed:
		return "FAIL"
	case legSkipped:
		return "SKIPPED"
	default:
		return "UNKNOWN"
	}
}

// legResult is the reported outcome of one leg, printed in the final
// summary table so a partial run's status is unambiguous at a glance.
type legResult struct {
	Name   string
	Status legStatus
	Detail string // human-readable reason (skip reason, or error text)
}

func (r legResult) String() string {
	if r.Detail == "" {
		return fmt.Sprintf("[%s] %s", r.Status, r.Name)
	}
	return fmt.Sprintf("[%s] %s — %s", r.Status, r.Name, r.Detail)
}

// shouldSkipGmailLeg decides whether round-trip (d) runs. Explicit
// -skip-gmail always skips it. Otherwise, per the Phase 3 instruction, a
// missing credentials.json (D-2 not yet satisfied) skips it with a clear
// SKIPPED message rather than crashing or blocking the ClearFacts legs.
func shouldSkipGmailLeg(explicitSkip, credentialsExist bool) (skip bool, reason string) {
	if explicitSkip {
		return true, "skipped via -skip-gmail"
	}
	if !credentialsExist {
		return true, "credentials.json not found (D-2 not yet satisfied) — Gmail leg deferred, ClearFacts legs still run"
	}
	return false, ""
}

// shouldSkipVerificationLegs decides whether round-trips (c) document-id
// verify and (c2) provenance readback run. They need a uuid from round-trip
// (b); when (b) itself is skipped (so the developer can re-run read-only
// legs without a second real upload), a uuid can still be supplied
// explicitly via -uuid to re-verify an earlier upload. Without either, (c)
// and (c2) have nothing to check and are skipped rather than failed.
func shouldSkipVerificationLegs(explicitSkip, uploadSkipped bool, uuid string) (skip bool, reason string) {
	if explicitSkip {
		return true, "skipped via flag"
	}
	if uuid != "" {
		return false, ""
	}
	if uploadSkipped {
		return true, "upload leg (b) was skipped and no -uuid was supplied to verify against"
	}
	return false, ""
}

// printSummary renders the final per-leg status table (used by main and
// tested indirectly through its inputs).
func printSummary(results []legResult) string {
	out := "spike summary:\n"
	for _, r := range results {
		out += "  " + r.String() + "\n"
	}
	return out
}

// anyFailed reports whether the summary contains a FAIL, which decides the
// spike's exit code — SKIPPED legs never fail the run on their own.
func anyFailed(results []legResult) bool {
	for _, r := range results {
		if r.Status == legFailed {
			return true
		}
	}
	return false
}
