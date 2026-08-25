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

**Parked messages are never aged out, never auto-approved, and never rendered in the review UI.**
A message that exhausts its failure budget is set aside so the poll can continue (F-70…F-79) — it is
not discarded. No prune, retention or tidy-up path may touch `message_failure`: forgetting a parked
message IS the silent miss the mechanism exists to prevent. It leaves the parked set only by being
processed successfully or by an explicit human `postbode retry`, which is the only manual recovery
path. It stays out of the review UI because extraction is precisely what failed, so there is no
document to review.

**The L1 bypass is retry-scoped and must stay that way.** `extract.Message.ForceReextract` exists
solely so a parked message can get past the F-30 message-id skip on a retry (F-78, ADR-005) —
without it, a message that failed *after* `RecordMessageIfNew` would be silently skipped forever
with no error and no log line. Set it only for an id admitted from the retry set: never for a listed
id, never globally, never stickily. It bypasses **L1 only**; L2, L3, L4 and F-44 rejection memory
stay in force.

## Scope fence

In: PRD P0 (spike) + P1 (MVP). Out: image conversion (P2), link-following (P3), auto-upload /
digest / doctor (P4), `SALE`/`VARIOUS` invoice types, the archive mutation, IMAP, and every Engie
platform artifact (no k8s, no Argo CD, no Dockerfile, no `vega.yaml`). Phase 16 adds the
developer's house release pipeline (GitVersion + GoReleaser + golangci-lint, see below) — this is
`michielvha`'s personal packaging convention, not an Engie CI system, and does not reopen the
"no CI config" fence for Engie tooling.

## Release engineering (Phase 16) — commit message contract

Versioning is driven entirely by conventional-commit prefixes on `main`, evaluated by GitVersion
(`gitversion.yml`) against every commit message (`commit-message-incrementing: Enabled`), not just
merge commits. Get the prefix right or the release tag is wrong:

| Prefix | Bump | Example |
|---|---|---|
| `feat:` / `feat(scope):` | **minor** | `feat(uploader): add retry backoff` |
| `fix:`, `perf:`, `refactor:`, `revert:` | **patch** | `fix(dedup): correct L2 sha256 comparison` |
| `BREAKING CHANGE` in the body, or `!:` after the type (`feat!:`, `fix!:`) | **major** | `feat!: drop vatnumber upload argument` |
| `chore:`, `docs:`, `style:`, `test:`, `ci:` | **no bump** | `docs: update README install steps` |

Tags are unprefixed (`0.1.4`, not `v0.1.4`). `.goreleaser.yml` builds `darwin/amd64` and
`darwin/arm64` only (never `windows`/`linux` — see the deviation comment in that file), zips them,
signs the checksum file with the developer's GPG key, and publishes a GitHub release. No container
image is built or pushed. Run `make lint` (golangci-lint, `.golangci.yml`) as part of the local
gate before pushing; CI (`.github/workflows/build-and-release.yaml`) runs `go vet`, `make test`,
`make test-nonet` and golangci-lint before tagging and releasing.
