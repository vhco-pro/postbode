---
description: "A message that exhausts its per-message failure budget is parked in its own message_failure table and the poll continues, with a bounded automatic retry that then goes quiet but stays reported forever, because an unbounded retry re-wedges the daemon on a schedule and an aged-out park is the silent miss G-1 exists to prevent."
status: proposed
date: 2026-08-25
author: "SDD Planner (automated), run by michielvha <michielvh@outlook.com>"
---

# ADR-004: Park-and-continue, with a bounded auto-retry that goes quiet but never goes away

## Status

Proposed — accompanies `plans/resilient-poll-failure-budget.md` (phases 1, 4, 6) and `docs/specs/resilient-poll-failure-budget.spec.md` (F-70…F-79).

## Context

`Watcher.Poll` returns out of its `for _, id := range msgIDs` loop on the first per-message error that is neither a re-auth condition nor a `404` (`internal/gmailwatch/poll.go:88`). That `return` happens **before** `SaveSyncState` at line 113, so `history_id` never advances, the next tick re-lists the same message, and it fails identically. One bad message is an unbounded mailbox-wide outage. Commit `c92fdb8` closed exactly one error shape — Gmail's `404` on `messages.get` — because for a 404 the reasoning is airtight. Every other shape (5xx, 429, spool write failure, malformed MIME, `decision_log` write failure) still wedges.

The design question is not *whether* to let the loop continue past a persistently failing message. It is **what the message becomes when the loop leaves it behind**, and that answer is constrained hard by G-1 (*silently dropping a real invoice is worse than letting a duplicate through*) and by the fact that ClearFacts publishes no document-list query, so Postbode can never ask the portal what it already holds. There is no external system that will notice a message Postbode forgets.

Three sub-decisions are entangled and are taken together here because taking any one of them alone produces a defensible-looking design that fails G-1:

1. Where the failure state lives.
2. Whether the automatic retry is unbounded.
3. Whether a park can ever age out.

## Decision

**A message that reaches `gmail.failure_budget` consecutive budget-consuming failures is *parked*: its state is written to a new `message_failure` table, the poll loop continues to the next id, and the cycle completes normally through `SaveSyncState`.** Parking is not dropping, and it is not the 404 skip.

Concretely:

- **State lives in its own table**, `message_failure`, keyed by `gmail_message_id`, added as a new forward-only entry in `internal/queue/schema.go`'s `migrations` slice. It carries **no foreign key to `message(gmail_message_id)`** and does not require a `message` row to exist, because the single most likely park cause is a failure at `users.messages.get` — before `internal/extract` has recorded anything at all. An FK here would look like good hygiene and would break the feature exactly where it is needed most.
- **Under budget, today's behaviour is unchanged**: the error aborts the poll, `sync_state` is not persisted, the next tick retries from the same `history_id`. That is what makes one 503 or one transient disk-full self-heal with no state, no notification and no human. The bound on this is structural — at most `failure_budget` cycles, default 3, ≈ 15 min at the 5-minute poll interval.
- **The automatic retry after a park is bounded**: first attempt due `park_retry_cooldown` (6h) after `parked_at`, doubling, capped at 24h, for at most `park_retry_attempts` (3) attempts. After that the message stays parked, stays listed by `postbode status`, stays notified-about-once, and generates no further automatic work. Only `postbode retry` schedules another attempt.
- **A message that has been parked once is thereafter processed with an effective budget of 1.** A single failure re-parks it immediately and the poll continues. A retry can therefore never re-wedge the poll.
- **Parked state never ages out.** No prune, retention or GC path may delete or hide it. It leaves the parked set only by processing successfully or by explicit human action. This is deliberately different from the spool prune policy (F-24, `retention_days`).

The parked set is surfaced by exactly two things: one notification per newly parked message, and a `postbode status` section that prints `parked messages:  0` when empty rather than disappearing.

## Alternatives Considered

| Option | Pros | Cons |
|---|---|---|
| **Skip the failing message and move on (extend `isMessageGone` to all errors)** | One-line change; the mailbox drains immediately | This is the silent miss G-1 forbids. A 5xx means the message *may still exist*; skipping it discards a real invoice with no error, no record and nothing for a human to act on. `isMessageGone`'s own doc comment already draws exactly this line. |
| **Retry forever with backoff, never park** | Nothing is ever given up on | Every retry of a permanently broken message costs another `failure_budget × poll_interval` stall, on a schedule — the 2026-08-22 outage on a 6-hourly timer. Also never escalates: the operator is never told, because nothing has "failed" yet. |
| **Columns on `message` instead of a new table** | No new table; failure state sits next to the message it describes | A `message` row does not exist for the most common park cause (`messages.get` 404/500 before extraction). Creating a placeholder `message` row to hold failure state would corrupt the L1 (F-30) message-seen semantics that `RecordMessageIfNew` guarantees — a message would become "seen" because it *failed*, which is the exact silent-miss shape ADR-001 protects against. |
| **Reuse `item`'s F-51 retry columns (`first_failed_at`, `retry_count`, `next_retry_at`)** | Zero schema change; one retry vocabulary | `item` rows are per *document*, and parking happens when there is no document — extraction is precisely what failed. Wrong cardinality and wrong lifecycle. The vocabulary is reused verbatim; the storage is not. |
| **Park, but prune parks older than `retention_days`** | Bounded `postbode status` output; symmetry with the spool prune | Forgetting a parked message **is** the silent miss the whole design exists to prevent. A 90-day-old park is not stale, it is unresolved. Rejected outright, not deferred. |
| **Dead-letter the message into the review queue as a row** | Reuses the existing review surface; visible where the human already looks | A parked message has no extractable document to review — extraction failed — so it would be a queue row with nothing to decide on, in a queue whose entire purpose is deciding on documents. It would also collide with the F-41 lifecycle, which has no state meaning "we could not read this". |
| **Park and notify on every subsequent poll that re-encounters it** | Impossible to ignore | Notification spam at the poll interval; the operator mutes notifications and the *next* real park is lost. F-16's persisted notify-once marker is the established precedent in this codebase. |

## Consequences

**Positive.** The stall becomes bounded by construction rather than by a timeout: `history_id` cannot fail to advance for more than `failure_budget` consecutive cycles, for any message and any error shape, and that is asserted directly (AC-34) rather than observed. A day-long upstream outage heals itself without a human. A permanently broken message stops generating work but never falls off the report. `postbode retry` is a pure SQLite write from a separate process — no lock file, no daemon IPC — because `queue.Open` already sets `journal_mode=WAL` and `busy_timeout=5000`.

**Negative.** A new table and a second retry vocabulary in the codebase, superficially similar to F-51's and easy to confuse. §5.2 of the spec carries the mapping table that keeps them aligned; the field names are deliberately identical where the meaning is identical. `postbode status` output grows unboundedly if many messages park and no human ever resolves them — accepted, because the alternative is hiding them.

**Constraint recorded.** The cooldown numbers (6h / ×2 / cap 24h / 3 attempts) are a judgement call, not a measurement — the only production data point is a single deleted message, whose class no longer reaches this path at all. They are config-tunable (`gmail.park_retry_cooldown`, `gmail.park_retry_attempts`), so this is wrong-but-tunable rather than wrong-and-stuck. Revisit after the first real park (spec OQ-70).

**Interaction recorded.** Parking advances `history_id` past the parked message by design, so it will never be listed again by `history.list` or by the F-13 fallback once that window rolls past its date. Retry therefore cannot depend on re-listing: the watcher prepends the due set to `msgIDs` at the start of every cycle. That admission path is the single mechanism behind both automatic and manual retry, and it is what makes ADR-005's L1 bypass necessary.
