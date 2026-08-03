---
description: "Duplicate prevention is built as four local layers that badge rather than suppress, because the ClearFacts API exposes no way to ask the portal what it already holds."
status: proposed
date: 2026-08-03
author: "SDD Planner (automated), run by michielvha <michielvh@outlook.com>"
---

# ADR-001: Four-layer local duplicate prevention, badge-don't-suppress

## Status

Proposed — accompanies `plans/postbode-gmail-invoice-agent.md` (phases 4 and 12).

## Context

The developer's stated pain is not "did it get sent" but **"was it already sent"** (spec §1). The obvious design — ask the portal what it already holds before uploading — is impossible:

- The complete ClearFacts GraphQL Query root is 10 queries (`administrations`, `administration`, `accountant`, `archiveCategories`, `associates`, `associateGroups`, `companyStatistics`, `document`, `customers`, `journals`) and **none of them lists or searches documents**. This is read off the full published schema, not inferred from documentation silence (spec OQ-4, closed).
- `document(id:)` is the only document read path, and the only way to obtain an id is from **your own upload response**. Postbode can therefore only ever see documents Postbode itself created.
- ClearFacts' own "possible duplicate" detection demonstrably exists in the UI, but it **flags rather than blocks**, and the schema exposes no duplicate/status/state field on `InvoiceDocument` or `File` — the flag is internal and API-invisible (spec OQ-7, closed negative).

Meanwhile, invoices reach the portal through channels Postbode cannot observe at all: Peppol, the portal's Auto-forward intake, the QPS mobile app, and manual drag-and-drop.

So a duplicate guarantee has to be built entirely locally, against an adversary (the portal's true contents) that cannot be queried.

The second forcing constraint is the **asymmetry of the two failure modes**. A false negative means a duplicate lands in the portal, gets flagged by ClearFacts, and someone deletes it — annoying, bounded, visible. A false positive means a real invoice is silently dropped and never booked — which violates G-1, is invisible by construction, and is discovered weeks later by an accountant. These costs are not close.

## Decision

Duplicate prevention is **four local layers**, and the two heuristic layers **never act on their own**:

| Layer | Signal | Action | Silent? |
|---|---|---|---|
| **L1** | Gmail `message_id` ledger in SQLite | Skip extraction entirely | Yes — exact, safe to automate |
| **L2** | SHA-256 of the extracted bytes | Store the item, link it via `linked_item_id`, never upload | Yes — exact, safe to automate |
| **L3** | Identity key `(vendor_domain, invoice_number, invoice_date, total_amount)`, fallback `(vendor_domain, year_month, total_amount)` | Stage with a `possible duplicate of <ref>` badge showing the match's status, uuid and date | **No — the human decides** |
| **L4** | Vendor taught "already in portal", or configured `known_peppol` | Stage pre-flagged with the reason and teaching date; Peppol vendors need an explicit override | **No — the human decides** |

L1 and L2 are exact predicates and may act silently. L3 and L4 are heuristics — L3 parses heterogeneous invoice text, L4 generalises from a single human observation about one vendor — and are therefore **restricted to changing what the review UI displays, never what gets uploaded or dropped**.

Two supporting mechanisms make this cheaper than it sounds. **Provenance stamping (F-56)**: every upload sets `tags: ["postbode"]` and a `comment` carrying the item id, Gmail message id and SHA-256 prefix, all readable back via `document(id:)`, so Postbode's own documents are self-identifying *inside* the portal. **Proof of delivery (F-37)**: every upload's uuid is verified with `document(id:)`, upgrading "we got a 200" to real confirmation.

**This is an invariant, not a default.** Any change that turns an L3 or L4 match into an automatic suppression requires a spec revision, not a code review.

## Alternatives Considered

| Option | Pros | Cons |
|---|---|---|
| **Query the portal before uploading** | Would be authoritative; no local state needed | **Impossible.** No list/search query exists in the published schema; `document(id:)` only resolves ids Postbode already owns. Closed finding, not an assumption. |
| **Rely on ClearFacts' server-side duplicate detection** | Zero code | It flags but does not block, so a double upload still creates a real document someone must clean up; and the flag is not readable via the API, so Postbode could never even learn its own verdict. Backstop only. |
| **L1 + L2 only (the PRD's three-layer design)** | Simpler; both layers are exact | Misses the two most common real cases: the same invoice re-sent as a regenerated PDF (different bytes), and an invoice that arrived via Peppol or Auto-forward. Leaves G-5 ("can I always answer 'is this already uploaded?'") unmet. |
| **Auto-suppress on an L3 identity-key match** | Fewer review clicks | Silently drops real invoices whenever the heuristic misfires — the expensive, invisible failure. Directly violates G-1. Rejected. |
| **Auto-suppress on L4 vendor teaching** | One click teaches forever | Over-generalises from one observation: a vendor that once delivered via Peppol may email the next one. Rejected for the same reason. |
| **Upload everything and let the accountant sort it out** | Trivial | Violates G-2 and the whole premise of the tool. |

## Consequences

**Positive.** The guarantee holds without any portal cooperation and survives ClearFacts schema changes. The two exact layers absorb the overwhelming majority of real duplicates with zero human effort. Provenance stamping makes channel attribution answerable from inside the portal, which reduces how often L4 teaching is needed at all. The worst realistic outcome of any layer failing is a flagged duplicate, not a lost invoice.

**Negative.** L3 and L4 generate review work rather than eliminating it — an L3 badge is a question, not an answer. L3's accuracy is unknown until the fixture corpus contains real invoices (OQ-6); if the `high`-confidence parse rate lands below 50%, the fallback key may carry too many false positives and need rethinking. L4 requires the human to teach each vendor once, and that teaching is a guess about the future.

**Structural.** The queue schema carries `identity_key`, `identity_confidence`, `identity_source`, `linked_item_id` and `dedup_layer` on every item so that the layer that fired is always attributable (F-38). A terminal status `duplicate_linked` is added to the F-41 lifecycle so L2 links can be stored without colliding with the partial unique index on `item.sha256` (plan OQ-P1). Requirements F-30 through F-39 collectively answer the developer's stated need and **none of them may be descoped without an explicit spec revision**.
