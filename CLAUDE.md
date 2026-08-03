# Postbode — agent notes

Gmail → ClearFacts/QPS purchase-invoice agent. Single-user macOS launchd daemon, Go, no cgo.

## Source of truth

- **Spec**: `docs/specs/postbode-gmail-invoice-agent.spec.md` (v1.3) — authoritative.
- **Plan**: `plans/postbode-gmail-invoice-agent.md` — the build sequence. Follow phases in order.
- **PRD**: `prd-postbode.md` — background only. **Where the PRD and the spec disagree, the spec wins.**
  The PRD is wrong about four things: the module path (`github.com/vhco-pro/postbode`, not
  `michielvh`), the upload argument (`companyNumber` — `vatnumber` is **deprecated**), image
  attachments (P2, not MVP), and the dedup layer count (four, not three).

## Hard rules

- **Secrets**: `CF_TOKEN` + `credentials.json` in dev, macOS Keychain in prod. NEVER commit tokens,
  `credentials.json`, the token cache, `session.token`, `*.db` or anything under `spool/`.
  `.gitignore` is the first commit on `main` and `make check-gitignore` enforces it — this repo has
  **no remote**, so a committed secret cannot be force-pushed away.
- **Tests must never hit `api.clearfacts.be` or `gmail.googleapis.com`.** Use `httptest` fakes and
  `testdata/*.eml` fixtures. The ONLY commands allowed to touch live APIs are `cmd/spike` and a
  future `postbode doctor`.
- **Live uploads land in a real accountant's queue.** Any test upload uses the filename prefix
  `TEST-postbode-ignore` and must be reported to the user with its `uuid` so they can delete it in
  the portal. "It appeared in the portal" is human-confirmed, never assumed.
- **The Gmail label `VH&Co/submitted` is resolved by exact full name, never created.** Only an
  authenticated `labels.list` that returns *without* the name counts as absent — an auth failure or
  pending re-auth is not "absent" and must not trigger the refusal path.
- Run `make test && go vet ./...` before declaring any task done.

## The invariant that matters most

Duplicate prevention is **four local layers** (L1 message-id, L2 SHA-256, L3 identity key, L4
teach-once vendor suppression). ClearFacts publishes **no document-list query**, so Postbode can
never ask the portal what it already holds — the guarantee is local by necessity, not by choice.

**L3 and L4 must NEVER auto-suppress.** They badge and let the human decide. Silently dropping a
real invoice is a worse failure than letting a duplicate through: ClearFacts flags duplicates
server-side, but nothing catches an invoice that was never staged. Do not "improve" a badge into an
automatic rejection.

**Every upload requires human approval.** There is no auto-approve code path in P1, and none should
be added.

## Scope fence

In: PRD P0 (spike) + P1 (MVP). Out: image conversion (P2), link-following (P3), auto-upload /
digest / doctor (P4), `SALE`/`VARIOUS` invoice types, the archive mutation, IMAP, and every Engie
platform artifact (no k8s, no Argo CD, no Dockerfile, no CI config, no `vega.yaml`).
