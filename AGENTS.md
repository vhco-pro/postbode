# Repository instructions

Read the personal conventions at
https://github.com/vhco-pro/docs/blob/e4d58d2cd69bd42f1efacf91dfdd3f01a24e134a/AGENTS.md
and follow the task routes there before implementation.

For all code, read conventions/coding.md at that revision.
To specify, plan or implement a feature, read conventions/feature-documents.md.
For Go, also read conventions/go.md; for CI and releases, conventions/delivery.md
and knowledge/shared-delivery.md. Edit prose with guides/humanize-prose.md.
When onboarding, read guides/onboard-a-repository.md at that revision.

Load only the relevant documents. If a required page is inaccessible, report it
and stop. Preserve the local instructions below and report any conflict with the
shared conventions.

## Development workflow

Use vh-adlc for this personal repository. Vega is reserved for Engie work and
must not be configured or invoked here.

Feature documents: docs/specs/

New work uses one `<slug>.md` feature document with the pinned vh-adlc format,
including its scope, design, criteria, tasks and review records. Use the installed
vh-adlc skills and verified binary to resolve knowledge, locate documents, lint,
review and check status changes. Do not create companion plans or summaries.

The existing `*.spec.md` files now use vh-adlc YAML frontmatter and required
sections. Preserve their paths and the `legacy_criterion_ids` mappings when
following older requirement references. `vh-adlc find --slug` discovers both
`.md` and `.spec.md` files with valid YAML frontmatter. Check the feature index
before creating a document and extend the existing document for the same topic.

The old `plans/`, `summaries/` and `decisions/` files retain historical phase
details and evidence. Feature documents link those records. Their
`legacy_status` field preserves earlier implementation claims; only `status`
controls the vh-adlc lifecycle. Never infer a vh-adlc review or acceptance from
an old completion claim. The initial format retrofit remains draft pending
review, even for behavior already implemented in the running app.

## Local commands and constraints

- `make build` builds the macOS daemon without cgo.
- `make test && go vet ./...` is the required local check, including lint.
- `make test-nonet` runs the suite with the existing no-live-network guard.
- `make e2e-dry` exercises the pipeline against local fakes.
- Keep the existing macOS LaunchAgent and Homebrew distribution. Framework
  onboarding does not authorize deploying or restarting the running app.

## Postbode constraints

Gmail → ClearFacts/QPS purchase-invoice agent. Single-user macOS launchd daemon, Go, no cgo.

### Source of truth

- **Spec**: `docs/specs/postbode-gmail-invoice-agent.spec.md` defines behavior.
- **Plan**: `plans/postbode-gmail-invoice-agent.md` is the original build sequence. Keep it as a legacy reference.
- **PRD**: `prd-postbode.md` supplies background only. **Where the PRD and the spec disagree, the spec wins.**
  The PRD is wrong about four things: the module path (`github.com/vhco-pro/postbode`, not
  `michielvh`), the upload argument (`companyNumber`; `vatnumber` is **deprecated**), image
  attachments (P2, not MVP), and the dedup layer count (four, not three).

### Hard rules

- **Secrets**: `CF_TOKEN` + `credentials.json` in dev, macOS Keychain in prod. NEVER commit tokens,
  `credentials.json`, the token cache, `session.token`, `*.db` or anything under `spool/`.
  `.gitignore` is the first commit on `main` and `make check-gitignore` enforces it: never rely on rewriting Git history to undo a leaked secret.
- **Tests must never hit `api.clearfacts.be` or `gmail.googleapis.com`.** Use `httptest` fakes and
  `testdata/*.eml` fixtures. The ONLY commands allowed to touch live APIs are `cmd/spike` and a
  future `postbode doctor`.
- **Live uploads land in a real accountant's queue.** Any test upload uses the filename prefix
  `TEST-postbode-ignore` and must be reported to the user with its `uuid` so they can delete it in
  the portal. "It appeared in the portal" is human-confirmed, never assumed.
- **The Gmail label `VH&Co/submitted` is resolved by exact full name, never created.** Only an
  authenticated `labels.list` that returns *without* the name counts as absent: an auth failure or
  pending re-auth is not "absent" and must not trigger the refusal path.
- Run `make test && go vet ./...` before declaring any task done.

### The invariant that matters most

Duplicate prevention is **four local layers** (L1 message-id, L2 SHA-256, L3 identity key, L4
teach-once vendor suppression). ClearFacts publishes **no document-list query**, so Postbode can
never ask the portal what it already holds: the guarantee is local by necessity, not by choice.

**L3 and L4 must NEVER auto-suppress.** They badge and let the human decide. Silently dropping a
real invoice is a worse failure than letting a duplicate through: ClearFacts flags duplicates
server-side, but nothing catches an invoice that was never staged. Do not "improve" a badge into an
automatic rejection.

**Every upload requires human approval.** There is no auto-approve code path in P1, and none should
be added.

**Parked messages are never aged out, never auto-approved, and never rendered in the review UI.**
A message that exhausts its failure budget is set aside so the poll can continue (F-70…F-79): it is
not discarded. No prune, retention or tidy-up path may touch `message_failure`: forgetting a parked
message IS the silent miss the mechanism exists to prevent. It leaves the parked set only by being
processed successfully or by an explicit human `postbode retry`, which is the only manual recovery
path. It stays out of the review UI because extraction is precisely what failed, so there is no
document to review.

**The L1 bypass is retry-scoped and must stay that way.** `extract.Message.ForceReextract` exists
solely so a parked message can get past the F-30 message-id skip on a retry (F-78, ADR-005) :
without it, a message that failed *after* `RecordMessageIfNew` would be silently skipped forever
with no error and no log line. Set it only for an id admitted from the retry set: never for a listed
id, never globally, never stickily. It bypasses **L1 only**; L2, L3, L4 and F-44 rejection memory
stay in force.

### Scope fence

In: PRD P0 (spike) + P1 (MVP). Out: image conversion (P2), link-following (P3), auto-upload /
digest / doctor (P4), `SALE`/`VARIOUS` invoice types, the archive mutation, IMAP, and every Engie
platform artifact (no k8s, no Argo CD, no Dockerfile, no Vega configuration). Phase 16 adds the
developer's house release pipeline (GitVersion + GoReleaser + golangci-lint, see below): this is
`michielvha`'s personal packaging convention, not an Engie CI system, and does not reopen the
"no CI config" fence for Engie tooling.

### Release engineering and commit messages

Versioning is driven entirely by conventional-commit prefixes on `main`, evaluated by GitVersion
(`gitversion.yml`) against every commit message (`commit-message-incrementing: Enabled`), not just
merge commits. Get the prefix right or the release tag is wrong:

| Prefix | Bump | Example |
|---|---|---|
| `feat:` / `feat(scope):` | **minor** | `feat(uploader): add retry backoff` |
| `fix:`, `perf:`, `refactor:`, `revert:` | **patch** | `fix(dedup): correct L2 sha256 comparison` |
| `BREAKING CHANGE` in the body, or `!:` after the type (`feat!:`, `fix!:`) | **major** | `feat!: drop vatnumber upload argument` |
| `chore:`, `docs:`, `style:`, `test:`, `ci:` | **no bump** | `docs: update README install steps` |

Tags are `v`-prefixed (`v0.6.3`); releases up to `0.6.2` are unprefixed. `.goreleaser.yml` builds `darwin/amd64` and
`darwin/arm64` only (never `windows`/`linux`: see the deviation comment in that file), zips them,
signs the checksum file with the developer's GPG key, and publishes a GitHub release. No container
image is built or pushed. Run `make lint` (golangci-lint, `.golangci.yml`) as part of the local
gate before pushing; CI (`.github/workflows/ci.yml`) runs `go-ci-action` (vet, test, golangci-lint
and more), `make check-gitignore` and `make test-nonet` before `vhco-pro/version-workflow` tags, and
the tag push publishes through `vhco-pro/go-release-workflow`.

Local delivery exception: `gitversion.yml` keeps `chore:` as no bump. This differs
from the pinned personal convention, whose bundled GitVersion configuration
patch-bumps `chore:`; the file is that configuration with only this exception.
