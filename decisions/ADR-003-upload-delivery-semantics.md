---
description: "Uploads are at-least-once with claim-in-transaction and are verified via document(id:), but an uploaded-but-unverified item is surfaced and never automatically retried."
status: proposed
date: 2026-08-03
author: "SDD Planner (automated), run by michielvha <michielvh@outlook.com>"
---

# ADR-003: Upload delivery semantics — at-least-once, claim-in-transaction, never retry the unverified

## Status

Proposed — accompanies `plans/postbode-gmail-invoice-agent.md` (phase 9).

## Context

The uploader sits between a durable local queue and a remote API that has no idempotency key, no upload-list query and no way to ask "did you already receive this file?". Three properties are in tension:

- **G-2** — nothing gets uploaded twice. Every duplicate is a real document in a real accountant's queue that a human must find and delete.
- **G-3** — nothing is uploaded without human approval. Approval must therefore be durable across a crash: making the human re-approve after every restart would make the tool worse than the manual process it replaces.
- **NF-06/NF-07** — the daemon must survive `kill -9`, laptop sleep and a 14-day offline period without losing or double-sending anything.

Three concrete failure shapes drive the decision:

1. **Crash between `approved` and the POST.** The item must upload on restart, exactly once, without a second approval (AC-18).
2. **Two Postbode processes started accidentally.** Both see the same `approved` rows. Without a claim protocol, both upload.
3. **The 200-but-unverified case.** The upload returns a `uuid`, but the follow-up `document(id: <uuid>)` does not resolve. Did it land? The API cannot say. Retrying might create a genuine second document; not retrying might leave a real invoice unsent.

Case 3 is the interesting one, because the intuitive engineering reflex — "retry until verified" — is wrong here.

## Decision

**Selector.** The uploader's item selector is literally `WHERE status='approved'`. There is no auto-approve code path anywhere in P1 (G-3 enforced structurally, not by policy).

**Claim-in-transaction.** Every item is claimed by a select-and-mark inside a single SQLite transaction that also re-checks dedup layers L1–L3. A concurrent or restarted uploader sees zero claimable rows rather than a race (F-53). This is the crux of both case 1 and case 2.

**At-least-once, made effectively exactly-once by dedup.** Approval is durable: a partial-batch failure leaves the remaining items `approved` and they retry automatically without re-approval (F-52). Delivery is at-least-once by construction; L1–L3 plus the claim transaction are what collapse it to effectively-once.

**Retry policy.** Network errors, 429 and 5xx retry with exponential backoff 1m→2h, giving up after 24h into terminal `failed` with the last error stored. 4xx other than 429 is terminal-`failed` immediately with `retry_count == 0` — a malformed request or a rejected file will not become valid by waiting. 401/403 additionally raises a notification naming a PAT or scope problem, so it cannot degenerate into a silent retry storm (F-51).

**Proof of delivery.** After every successful upload the returned `uuid` is stored, then `document(id: <uuid>)` is called and `verified_at` recorded on a resolving response (F-37). This is the only read the API permits and it upgrades "we got a 200" to actual confirmation.

**Never retry the unverified.** An item with a `uuid` but no `verified_at` is displayed as `uploaded (unverified)`, surfaced in `postbode status`, and **is not retried automatically**. A retry risks creating a real duplicate in the portal — which violates G-2 harder than an unverified row violates anything. The correct resolution is a human looking at the portal, which is exactly what the `uploaded (unverified)` display asks for.

**Label move is downstream of terminal upload.** `messages.modify` (add `vh&co/submitted`, remove `INBOX`) fires only once **all** documents extracted from a message reach terminal `uploaded` — exactly one modify call per message, never on a message with a non-terminal document (F-14).

## Alternatives Considered

| Option | Pros | Cons |
|---|---|---|
| **Retry until `document(id:)` verifies** | Feels complete; no unverified rows left dangling | Each retry can create a **real second document** in the accountant's queue, since the API has no idempotency key and no way to detect its own prior receipt. Trades a visible unknown for an invisible duplicate. Rejected — directly violates G-2. |
| **Advisory lock / PID file instead of claim-in-transaction** | Simple to write | Does not survive `kill -9` cleanly (stale lock), and does not compose with the L1–L3 re-check that must happen atomically with the claim. SQLite already gives the transaction for free. |
| **Exactly-once via an idempotency key** | The textbook answer | **ClearFacts offers no idempotency key.** Not available. |
| **Require re-approval after any crash** | Trivially safe against double upload | Destroys G-3's usability: the developer would re-approve after every sleep/restart cycle. Makes the tool worse than the manual process. |
| **Retry 4xx with backoff too** | Uniform policy, less branching | A 400 or 422 will not become valid by waiting; it burns the 24h window and delays the `failed` notification that tells the developer something needs fixing. |
| **Move the Gmail label immediately on upload response** | Simpler ordering | A message with 2 PDFs where 1 succeeded would leave the INBOX with an unsent invoice inside it — precisely the G-1 silent-loss failure. |

## Consequences

**Positive.** Approval survives crashes and accidental double-starts without human re-work. The retry policy distinguishes transient from terminal failure, so the developer is notified about the things that actually need a human. Proof of delivery means "uploaded" in Postbode's record means the portal confirmed it, not that an HTTP call returned 200.

**Negative.** The `uploaded (unverified)` state is a deliberate dead end: it requires a human to look at the portal, and there is no automated path out of it. That will feel unfinished, and a future contributor will be tempted to "fix" it by adding a retry. **They must not** — the plan and this ADR exist so that temptation is answered before it is acted on.

**Structural.** `retry_count`, `next_retry_at`, `last_error`, `failed_at`, `uuid`, `amount_of_pages` and `verified_at` all live on the `item` row so the entire delivery history is reconstructible offline (F-38, G-5). AC-17 (503×3 then 200 → one uuid, `retry_count == 3`; 400 → `failed`, `retry_count == 0`) and AC-18 (kill between `approved` and upload → uploads exactly once) are the proofs of this ADR.
