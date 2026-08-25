---
description: "A retry of a parked message bypasses the L1 message-id skip for that one attempt, rather than making record-and-stage atomic or deleting the message row, because the record-before-stage window in extract.ExtractMessage can otherwise turn a parked invoice into a permanent, completely silent miss."
status: proposed
date: 2026-08-25
author: "SDD Planner (automated), run by michielvha <michielvh@outlook.com>"
---

# ADR-005: A retry bypasses L1 for exactly one attempt

## Status

Proposed — accompanies `plans/resilient-poll-failure-budget.md` (phase 5) and `docs/specs/resilient-poll-failure-budget.spec.md` (F-78, AC-45). Refines ADR-001 (four-layer local duplicate prevention) without weakening it: L2, L3, L4 and F-44 rejection memory are untouched.

## Context

`extract.ExtractMessage` (`internal/extract/extract.go:132-207`) runs in this order, and the ordering is deliberate — it is the spec §8 disk-full fail-safe:

1. `MessageSeen` — cheap L1 skip, returns `Skipped: true` for a message already recorded (line 141).
2. Spool every candidate PDF to disk; a failure here removes what was written, returns an error, and the message is **never** marked seen (lines 165-176).
3. `RecordMessageIfNew` — mark the message seen (line 182).
4. `StageItem` per candidate — commit the queue rows (after line 211).

The fail-safe closes the window between spooling and recording. **It does not close the window between step 3 and step 4.** A failure after `RecordMessageIfNew` succeeds but before or during staging — a `decision_log` write failure surfaced through the rules gate, a SQLite error on `StageItem`, a context cancellation, a process kill — leaves the message recorded as seen with zero queue rows. Verified by reading the code, not inferred.

Today that window is survivable-ish, because such a failure aborts the poll and nothing else changes. Under ADR-004 it becomes lethal: the message accrues failures, parks, and is retried. Every retry hits the L1 check at step 1, returns `Skipped: true`, logs `skip (L1)`, and reports **success**. The park clears. The message disappears from `postbode status`. There is no error, no park churn, no log line that reads like a problem — and the invoice is gone forever.

This is the sharpest correctness hazard in the feature and the only one that is invisible from the outside. G-1 ranks it worst: no operator would ever notice.

## Decision

**A retry of a parked message — automatic (F-75) or manual (F-77) — bypasses the L1 message-id skip for that single attempt, and for nothing else.**

Mechanically: `extract.Message` gains a `ForceReextract bool` (name to be finalised in implementation), set by `Watcher.processMessage` **only** for ids that came from the retry admission set for this cycle. When set, `ExtractMessage` skips the `MessageSeen` early return at line 141 and the `alreadySeen` concurrent-race return at line 195, and proceeds to spool, record (idempotently) and stage.

Everything else in the dedup stack stays fully in force for that attempt:

- **L2 (SHA-256)** — a byte-identical document links as `duplicate_linked` against the existing partial unique index on `item.sha256`, exactly as AC-11 requires. Re-extraction therefore cannot produce a second *uploadable* copy.
- **L3 (identity key)** and **L4 (vendor teaching)** — unchanged; they badge, they never auto-suppress (CLAUDE.md).
- **F-44 rejection memory** — a `(gmail_message_id, sha256)` pair already rejected stays unstageable. A retry cannot resurrect a rejected document.

The flag is per-attempt and per-message, never global, never sticky, and never set for an ordinarily listed message.

## Alternatives Considered

| Option | Pros | Cons |
|---|---|---|
| **Do nothing; rely on the spool-before-record ordering** | Zero change | The window is real and reachable: `RecordMessageIfNew` at line 182 runs before `StageItem`. A failure in between leaves the message permanently L1-skipped. This is the defect AC-45 exists to catch. |
| **Make record + stage one transaction** | Structurally closes the window with no new flag; arguably the "right" fix | Requires a transaction spanning `internal/extract` and `internal/queue` across the rules gate, which calls `EvaluateAndRecord` and writes `decision_log` mid-flight. That reopens `StageItem`'s L2/L3/L4/F-44 logic and the `item_transition` writes — the highest-value, most-tested code in the repo — for a problem the bypass solves at the edge. It also cannot help a message parked *before* extraction ever ran, which is the more common case, so the bypass would still be needed. Rejected as high blast radius, incomplete coverage. |
| **Delete the `message` row when parking** | Restores a clean slate; no new flag; L1 then works normally on retry | Deleting a `message` row orphans any `item` rows that *did* stage (the FK `item.gmail_message_id REFERENCES message(gmail_message_id)`), and destroys the `first_seen_at` and `labeled_at` audit trail. It also makes L1 lie in the other direction: a genuine Gmail history replay of that id would re-extract from scratch. Trades a silent miss for a silent duplicate — a better trade, but still a wrong one. |
| **Record the message only after all staging succeeds** | Closes the window by reordering | Reintroduces the failure mode the current ordering was written to prevent: a crash between staging and recording re-extracts and re-spools everything on the next poll, and F-44/L2 are the only things standing between that and duplicate rows. The current order was chosen deliberately; ADR-001 depends on it. |
| **Bypass L1 for every parked message on every poll, not just admitted retries** | Simpler predicate | Leaks the bypass into ordinary polls, defeating F-30's replay protection for exactly the ids most likely to be re-listed by an F-13 fallback resync. The bypass must be scoped to the attempt, not to the message's state. |

## Consequences

**Positive.** A parked message is genuinely recoverable rather than merely re-attempted. The only path that can weaken L1 is one that a human or a bounded auto-retry explicitly asked for, on one message, for one attempt. `internal/extract`'s spool-before-record fail-safe and `internal/queue`'s dedup logic are untouched.

**Negative.** L1 acquires a conditional, and a conditional on a dedup layer is exactly the kind of code that erodes. Two mitigations are mandatory and are plan tasks, not suggestions: the flag must be documented at its declaration with a pointer to this ADR, and AC-45 must assert the *positive* case (documents reach the queue) **and** the two negative cases (no second uploadable item for a byte-identical document; a rejected document stays rejected). AC-45 is the only thing standing between this design and the defect it was written to prevent — it may not be descoped.

**Constraint recorded.** If `internal/extract`'s ordering ever changes so that recording and staging become atomic, this ADR should be revisited: the bypass would still be needed for messages parked *before* extraction, but its justification narrows, and a narrower justification should produce a narrower flag.
