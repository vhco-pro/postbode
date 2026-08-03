# PRD — "Postbode": Gmail → QPS (ClearFacts) invoice agent

**Status:** Draft v1.2 (agent-build ready — Claude Code implementation notes in §13) · **Date:** 2026-08-03 · **Owner:** Michiel · **Target platform:** macOS (background daemon) · **Language:** Go

---

## 1. Problem

Supplier invoices arrive scattered across a Gmail inbox — some as PDF attachments, some as "your invoice is ready" links, some as photos/screenshots. Getting them into the QPS bookkeeping portal is manual: find the email, save the file, drag it into the portal (or forward it by hand). Things get missed, uploaded late, or uploaded twice.

## 2. Recon findings (what we're actually integrating with)

The QPS portal at `app.myqps.be` is **ClearFacts**, white-labeled per accounting firm. Evidence: portal assets are served from `assets-prod.cdn.clearfacts.be`, and the "QPS Accountants" mobile app is published under the package id `cf.clearfacts.qps` ("QPS Accountants by ClearFacts").

This matters because ClearFacts has a **public, documented API** — no scraping or reverse-engineering of the QPS web UI is needed.

### 2.1 ClearFacts API essentials

| Aspect | Detail |
|---|---|
| Endpoint | `https://api.clearfacts.be/graphql` (GraphQL, single endpoint) |
| Auth | Personal Access Token (80 ASCII chars, **non-expiring**, Bearer header) — designed exactly for offline/desktop tools like this. OpenID Connect also exists but is meant for multi-user SaaS. |
| Token creation | **Confirmed available in the QPS white-label** (verified 2026-08-03, Administrator profile): profile page → *Persoonlijke toegangstokens* → *Nieuwe token aanmaken* → name + scopes → token shown once. Required scopes: **"Een document uploaden in jouw naam"** (upload) and **"De lijst met KMO's opvragen waar je toegang tot hebt"** (lets the tool auto-discover the administration + VAT number). The *Integraties* section on the same page is only the Dropbox connector — not needed. |
| Upload | `uploadFile` mutation, sent as `multipart/form-data`: the GraphQL `query`, a `variables` JSON (`vatnumber`, `filename`, `invoicetype`), and the binary under field name `file`. |
| Invoice types | `PURCHASE` (aankoop) / `SALE` (verkoop). A separate archive-upload mutation exists for "Divers" documents. |
| Accepted files | `application/pdf`, `image/jpeg`, `application/xml` (UBL / Billing3 / UBL.BE / E-FFF) |
| Response | `{ uuid, amountOfPages }` — the uuid is our proof-of-delivery and dedup anchor |
| Destination | The digital inbox ("Digitale postbus") of the administration identified by VAT number — i.e. exactly the "In verwerking" list in the screenshot. The VAT number is a required mutation parameter because a token belongs to a *person* who may access multiple KMOs; the tool queries the KMO list once at first run, picks the VH & Co administration, and stores its VAT number in config automatically. |
| Support | `dev-support@clearfacts.be` |

Reference upload call:

```bash
curl -H "Authorization: Bearer $CF_TOKEN" \
  -X POST https://api.clearfacts.be/graphql \
  -F 'query=mutation uploadFile($vatnumber: String!, $filename: String!, $invoicetype: InvoiceTypeArgument!) { uploadFile(vatnumber: $vatnumber, filename: $filename, invoicetype: $invoicetype) { uuid amountOfPages } }' \
  -F 'variables={"vatnumber":"BE0xxx.xxx.xxx","filename":"telenet-2026-07.pdf","invoicetype":"PURCHASE"}' \
  -F file=@invoice.pdf
```

### 2.2 Context that shrinks the problem

Belgium's B2B e-invoicing mandate (Peppol) took effect in January 2026. Belgian suppliers increasingly deliver invoices straight into the portal via the Peppol channel — the two Acerta lines in the screenshot ("API / Peppol QPS") are exactly that. The email problem is therefore mostly **foreign vendors, SaaS subscriptions, webshops, and consumer-style suppliers** that don't do Peppol. That's the corpus this tool targets; it should never touch what Peppol already handles.

The screenshot also shows an existing **Auto-forward** channel — some Gmail forwarding to the portal's unique intake address is already active. Postbode must coexist with it without producing duplicates (see §7, Dedup, and §11, Open questions).

## 3. Goals

Deliver every purchase-invoice email from the Gmail account into the QPS portal with zero manual file handling, while keeping a human approval step. Concretely: nothing matching the rules gets missed; nothing gets uploaded twice; nothing is uploaded without Michiel having approved it; the tool runs unattended on the MacBook and recovers cleanly from sleep, restarts, and offline periods.

## 4. Non-goals

No sales-invoice handling (v1). No bookkeeping logic — coding, VAT treatment, and approval-for-payment stay with QPS and ClearFacts' own recognition pipeline. No replacement of the Peppol channel. No iPhone capture flow (the existing QPS mobile app already covers photographed receipts). Not a general email client or a hosted service — this is a single-user local daemon.

## 5. User & usage story

One user (Michiel), one Gmail account, one administration ("VH & Co", one VAT number). The daemon polls Gmail in the background. When it finds candidate invoices, it stages them and sends a macOS notification: *"Postbode: 4 invoices waiting for review."* Michiel opens the local review page (`http://localhost:7391`), sees sender / subject / extracted file preview / proposed filename, unticks anything wrong, hits **Approve & upload**. Approved documents appear in the portal's "In verwerking" list within seconds, marked with upload receipts (uuid) in Postbode's log. Rejected items are remembered and never resurface.

## 6. Architecture

```
┌──────────────────────────── macOS (launchd LaunchAgent) ───────────────────────────┐
│                                                                                    │
│  Gmail watcher ──► Extractor ──► Rules engine ──► Review queue (SQLite) ──► Uploader│
│   (poll/IMAP)     (MIME walk,     (allow/deny,      + local web UI          (ClearFacts│
│                    link fetch,     classify)         + notification)         GraphQL) │
│                    img→conv)                                                        │
│                                                                                    │
│  State: SQLite (queue, dedup hashes, upload receipts) · Secrets: macOS Keychain    │
└────────────────────────────────────────────────────────────────────────────────────┘
```

Single static Go binary, three run modes: `postboded` (daemon, launched by a `LaunchAgent` plist with `KeepAlive`), `postbode review` (opens the web UI), `postbode status/log` (CLI inspection). SQLite via `modernc.org/sqlite` (pure Go, no cgo). Secrets (Gmail credentials, ClearFacts PAT) in the macOS Keychain via `go-keyring`; nothing sensitive in the config file.

### 6.1 Gmail access — decision

Two viable options; **start with the Gmail API** but know the trade-off:

**Gmail API (recommended).** OAuth desktop flow, scopes `gmail.readonly` + `gmail.modify` (modify only for labels). On successful upload the message is moved to the **existing `vh&co/submitted` "folder"** — which in Gmail is a nested label (`submitted` under `vh&co`; IMAP clients render nested labels as folders). "Move" = apply that label + remove `INBOX` via `messages.modify`. Implementation note: resolve the label ID by listing labels and matching the full name `vh&co/submitted` at startup; if not found, **fail loudly** — never auto-create a lookalike label. All other state (seen, staged, rejected) lives in SQLite only; nothing else in the mailbox is touched. Poll every 5 minutes using incremental `history.list` sync from a stored `historyId`, falling back to a `messages.list` query (`has:attachment OR (invoice OR factuur)` + date window) on first run or history gaps. Caveat: Gmail scopes are "restricted", so a personal Google Cloud OAuth client stays in *Testing* mode → refresh tokens expire every 7 days, or you flip the consent screen to *Production* unverified and click through the warning once. Annoying but workable for a personal tool; the PRD accepts the one-time consent-screen fiddling.

**IMAP + app password (fallback).** If the OAuth ceremony proves irritating: enable 2FA, mint an app password, use IMAP IDLE. Simpler auth, but state must live entirely in SQLite (no labels) and the password grants full mailbox access rather than read-only. Keep this as a documented escape hatch, not the default.

### 6.2 Extractor — the three email shapes

**PDF attachments (MVP).** Walk the MIME tree, collect `application/pdf` parts (including inside `multipart/mixed` nesting and `.pdf` named `application/octet-stream`). Reject password-protected PDFs into the review queue with a "needs manual handling" flag.

**Image attachments (MVP).** JPEG passes through untouched (ClearFacts accepts it). PNG/HEIC/TIFF are converted on-device with macOS's built-in `sips` (`sips -s format jpeg in.heic --out out.jpg`) — zero extra dependencies, and HEIC is exactly what iPhone photos produce. Multi-image emails become one queue item per image. Tiny images (< 30 KB — signature logos, tracking pixels) are discarded.

**Link-based invoices (Phase 3 — the honest hard part).** Emails saying "view your invoice". Tiered strategy: tier 1, direct links whose response is `application/pdf` (many billing systems: Stripe, Mollie receipts) — plain HTTP GET with cookies/redirect handling. Tier 2, per-vendor recipes — small YAML-defined fetchers (regex to pull the link, optional extra hop) for your actual recurring vendors, added one at a time as they show up in the "unhandled" report. Tier 3, anything needing login/portal navigation — **out of scope**; the queue item is kept with a deep link to the email so it's a one-click manual job, and it's counted in a weekly "vendors worth a recipe" digest. We do not build a headless browser into v1–v3.

### 6.3 Rules engine

Config-driven (`~/.config/postbode/config.yaml`), evaluated per extracted document, first match wins:

```yaml
administration:
  vatnumber: auto                   # discovered via the KMO-list query on first run
gmail:
  query_window_days: 30
  poll_interval: 5m
  submitted_label: "vh&co/submitted"   # existing label; applied after successful upload
rules:
  - match: { from: "*@ovh.com", has: pdf }        # allow: straight to queue
  - match: { from: "*@github.com", subject: "receipt" }
  - deny:  { from: "*@newsletter.*" }               # never even queue
  - deny:  { list_unsubscribe: true, has_no: pdf }  # marketing heuristic
default: queue_if(pdf_attachment || image_attachment || invoice_keywords)
```

The default heuristic (attachment present + invoice-ish keywords in subject/body: *factuur, invoice, receipt, creditnota, rekening* …) keeps recall high; the review queue makes precision Michiel's one-click job instead of a parsing problem. Every decision (queued / denied / no-match) is logged so rules can be tuned from evidence.

### 6.4 Review queue & UI

Given the "review queue" requirement: staged items live in SQLite with the source email metadata, extracted file, SHA-256, proposed filename (`{vendor}-{date}-{orig}.pdf`), and status (`staged → approved → uploaded | rejected | failed`). The UI is a single embedded local web page (Go `html/template` + `embed`, no build step, bound to `127.0.0.1`): list view with inline PDF/image preview, per-item approve/reject, and one **Approve all & upload** button. Notifications via `osascript` when new items are staged and when uploads complete. A `--yolo` config flag exists to flip specific rules to auto-upload later, once trust is earned — the architecture treats autonomy as per-rule, not global.

### 6.5 Uploader

For each approved item: POST the `uploadFile` mutation (multipart), `invoicetype: PURCHASE`, with exponential backoff (1m → 2h, max 24h) on network/5xx errors; store the returned `uuid`, then move the source message to `vh&co/submitted`. An email is only moved once *every* document extracted from it is uploaded. Partial-batch failures leave remaining items `approved` and retry automatically — approval is durable, upload is at-least-once with dedup making it effectively exactly-once.

## 7. Dedup — the rule that keeps QPS happy

Three layers: (1) Gmail message-id — an email is only ever processed once, tracked in SQLite; (2) content hash — SHA-256 of the extracted file; a hash already uploaded (or staged) is silently linked to the earlier item, which handles vendors that re-send and the same invoice arriving via two emails; (3) coexistence with other intake channels — confirmed there are **no Gmail-side forwarding filters today**, so the "Auto-forward" rows visible in the portal come from an intake path outside the Gmail account (likely a QPS-configured forward on another mailbox); layers 1–2 still protect against any overlap. ClearFacts' own recognition also dedups on their side, but we don't lean on it.

## 8. Security & privacy

Gmail scope is read-only plus label-writes; the ClearFacts PAT is scoped to upload at token creation. Both live only in the macOS Keychain. Extracted files are kept in `~/Library/Application Support/Postbode/spool/` and pruned N days after successful upload. The web UI binds to localhost only, with a random session token so other local processes can't approve uploads. No telemetry, no cloud component; logs are local, rotated, and never contain message bodies.

## 9. Failure modes & observability

Laptop asleep at poll time → next poll catches up via history sync (the query window is 30 days, so even a two-week holiday is covered). Gmail token expired → notification with a one-click re-auth URL; daemon keeps spooling nothing rather than crashing. ClearFacts API down → backoff, queue intact. Ambiguous email → staged with a "low confidence" badge, never dropped silently. `postbode status` prints: last poll, queue counts, last upload uuid, and any items stuck > 48h — the same summary lands in a weekly digest notification.

## 10. Milestones

| Phase | Scope | Definition of done |
|---|---|---|
| **P0 — Spike (automated, first coding session)** | `go run ./cmd/spike` — a throwaway command Claude Code writes and runs first: (a) query the KMO list and print administrations + VAT numbers, (b) upload one clearly-marked test PDF (`TEST-postbode-ignore.pdf`) as PURCHASE, print the returned uuid, (c) list the 5 newest Gmail messages and resolve the `vh&co/submitted` label ID. | All three round-trips succeed against the real accounts from within the dev environment. Human does only the two one-time credential steps (§13). Warn QPS a test document is coming, or delete it from "In verwerking" right after. |
| **P1 — MVP (core value)** | Daemon + Gmail poll + PDF attachment extraction + rules + SQLite queue + web review UI + uploader + dedup + launchd + Keychain. | Two weeks of real invoices flow through review → portal with zero manual file handling. |
| **P2 — Images** | HEIC/PNG→JPEG via `sips`, tiny-image filtering. | Photographed receipts forwarded to the Gmail account arrive in the portal. |
| **P3 — Link invoices** | Tier-1 direct-PDF links + per-vendor recipe framework + "vendors worth a recipe" digest. | Top 3 recurring link-based vendors automated. |
| **P4 — Comfort** | Per-rule auto-upload, weekly digest, `postbode doctor` self-check. | Review time < 2 min/week. |

## 11. Open questions

~~1. Does the QPS white-label expose token creation?~~ **Resolved 2026-08-03: yes** — *Persoonlijke toegangstokens → Nieuwe token aanmaken* is available on the Administrator profile, with an upload scope ("Een document uploaden in jouw naam") and a KMO-list scope.

~~2. VAT number?~~ **Resolved by design** — auto-discovered at first run via the KMO-list query (that's why the second scope is selected); config just needs confirmation if more than one administration comes back.

~~3. Existing Gmail forwarding filters?~~ **Resolved: none exist.** Remaining curiosity, non-blocking: ask QPS where the portal's "Auto-forward" rows originate, purely so we know which channel owns which vendors.

Still open:

1. **Which Gmail account/label is the watch scope** — whole inbox, or a label? (Personal mail mixed in argues for watching a label or the whole inbox with rules; the review queue makes either safe.)
2. **ClearFacts rate limits / max file size** — not documented; confirm with `dev-support@clearfacts.be` before assuming (we'll be doing single-digit uploads per day, so almost certainly fine).

## 12. Alternatives considered

**Plain Gmail filters → ClearFacts intake email (zero code).** Already partly in place; rejected as the full solution because it offers no review step, no link-following, no image conversion, no dedup, and forwards marketing noise. It remains the benchmark: Postbode must beat "just add more filters" on missed-invoice rate to justify existing.

**Apple Mail / Mail.app scripting.** Fragile (AppleScript, mail must be synced locally, breaks across macOS updates) and adds nothing over talking to Gmail directly.

**Scraping the QPS web UI for upload.** Unnecessary given the documented GraphQL API; would break on every ClearFacts frontend release.

**Hosted (cloud) service instead of a Mac daemon.** More reliable uptime, but the explicit requirement is a local background tool; the 30-day catch-up window makes laptop-only acceptable.

## 13. Implementation notes — building this with Claude Code in VS Code

This PRD is the working spec for an agentic build: Claude Code implements, runs, and verifies each phase itself, including the P0 spike. Everything below exists so a coding agent can proceed without asking questions.

### 13.1 One-time human prerequisites (the only manual steps)

Two credentials cannot be created by an agent; do these once before the first session and export them as environment variables in the dev shell:

1. **ClearFacts PAT** — app.myqps.be → profile → *Persoonlijke toegangstokens* → *Nieuwe token aanmaken*, scopes *"Een document uploaden in jouw naam"* + *"De lijst met KMO's opvragen waar je toegang tot hebt"*. Export as `CF_TOKEN` (80 chars).
2. **Google OAuth desktop client** — console.cloud.google.com → new project → enable Gmail API → OAuth consent screen (External; either add yourself as test user, accepting 7-day refresh-token expiry during dev, or set to Production and click through the unverified warning once) → create *Desktop app* credentials → save as `credentials.json` in the repo root (gitignored). The first `spike`/`doctor` run prints an auth URL; the human clicks consent once; the refresh token is then cached.

After that, Claude Code can run every live command itself.

### 13.2 Repo layout

```
postbode/
├── CLAUDE.md                  # agent instructions (see 13.3)
├── cmd/
│   ├── postbode/              # main binary: daemon + review + status subcommands (cobra or stdlib flag)
│   └── spike/                 # P0 throwaway; delete after P1 ships
├── internal/
│   ├── gmailwatch/            # OAuth, history sync, message fetch, label move
│   ├── extract/               # MIME walk, attachment harvest, sips conversion, link tiers
│   ├── rules/                 # config-driven matching
│   ├── queue/                 # SQLite store, item lifecycle
│   ├── clearfacts/            # GraphQL client: kmo list query + uploadFile multipart
│   ├── webui/                 # embedded review UI (html/template + embed.FS)
│   └── keychain/              # go-keyring wrapper; env-var fallback for dev
├── testdata/                  # .eml fixtures, sample PDFs/HEICs, golden files
├── Makefile                   # build / test / spike / install-launchagent
└── launchd/be.vhco.postbode.plist
```

Module `github.com/michielvh/postbode` (adjust). Key deps: `google.golang.org/api/gmail/v1`, `golang.org/x/oauth2`, `modernc.org/sqlite`, `github.com/zalando/go-keyring`. No cgo, no web framework, no JS build step.

### 13.3 CLAUDE.md starter

```markdown
# Postbode — agent notes
- Spec: prd-postbode.md at repo root. Follow its phases; do not skip the review-queue requirement.
- Secrets come from env (CF_TOKEN, credentials.json) in dev, macOS Keychain in prod. NEVER
  commit tokens, credentials.json, token cache, or anything in spool/. Check .gitignore first.
- Tests must never hit api.clearfacts.be or gmail.googleapis.com. Use httptest servers and
  testdata/*.eml fixtures. The ONLY commands allowed to touch live APIs: cmd/spike, `postbode doctor`.
- Live uploads land in a real accountant's queue. Any test upload uses filename prefix
  TEST-postbode-ignore and must be reported to the user so they can delete it in the portal.
- The Gmail label vh&co/submitted must be resolved by name, never created.
- Run `make test && go vet ./...` before declaring any task done.
```

### 13.4 Test strategy

Unit-test the pure parts hard: MIME extraction against a growing `testdata/` corpus of real (sanitized) invoice emails saved as `.eml`; rules engine as table tests; dedup logic against replayed histories. Integration-test `clearfacts/` and `gmailwatch/` against `httptest` fakes that mimic the GraphQL multipart contract and Gmail pagination/history semantics. `sips` calls are behind an interface with a fake for CI (real `sips` only in a `//go:build darwin` smoke test). End-to-end: a `make e2e-dry` target runs the daemon against a fixture mailbox fake with uploads pointed at a local fake server — the full pipeline without touching anything real.

### 13.5 Live verification etiquette

The spike and any live `doctor` run touch production systems (a real mailbox, a real accountant queue). Rules: read-only Gmail calls are always fine; label moves only on messages the pipeline actually processed; every live upload is announced in the session output with its uuid and filename so Michiel can spot-check "In verwerking"; test uploads always use the `TEST-postbode-ignore` prefix. Claude Code should treat "it appeared in the portal" as human-confirmed, not assumed.
