# Postbode

Postbode is a single-user macOS background daemon that watches one Gmail
inbox, extracts purchase-invoice PDF attachments, applies a config-driven
rules engine, and stages candidates in a local review queue. Nothing is ever
uploaded without explicit human approval; approved items are POSTed to the
ClearFacts/QPS GraphQL API (`uploadFile`, `invoicetype: PURCHASE`) into the
firm's digital inbox, and the source Gmail message is labelled
`VH&Co/submitted`.

It serves exactly one user, one mailbox, one administration, one Mac. There
is no cloud component, no telemetry, no hosted service, no container image.
Secrets live in the macOS Keychain (with an env-var fallback for local dev
only); everything else is a local SQLite database and a local spool
directory.

Source of truth for behaviour: `docs/specs/postbode-gmail-invoice-agent.spec.md`.
Build sequence: `plans/postbode-gmail-invoice-agent.md`. Agent-facing rules:
`CLAUDE.md`.

## Human prerequisites

Two things cannot be created by automation and must exist before Postbode
can do anything:

1. **A ClearFacts personal access token (PAT)**, minted with the scopes
   `upload_document`, `read_administrations`, and `statistics` (scopes are
   fixed at creation time — omit one now and you mint a new token later).
   In development, export it as `CF_TOKEN` (or put it in a gitignored
   `.env`); in production it is read from the macOS Keychain.
2. **A Google OAuth client, registered as a Desktop app, User type
   Internal.** Internal (not External/Testing) matters: Google's 7-day
   refresh-token expiry only applies to external apps in Testing status, and
   Internal sidesteps it entirely as long as the watched mailbox is inside
   your Google Workspace org. Save the downloaded client secret as
   `credentials.json` in the repo root (gitignored).
3. **The Gmail label `VH&Co/submitted` must already exist**, spelled and
   cased exactly like that. Postbode resolves it by exact name (falling
   back to a case-insensitive match) and refuses to start rather than
   create a lookalike label — an absent label is a configuration error, not
   something to paper over.

Nothing else can proceed without these three.

## Install

Postbode ships as `darwin/amd64` and `darwin/arm64` binaries only — see
[Release engineering](#release-engineering-and-versioning) for why. Build
and install the launchd LaunchAgent locally:

```sh
make install-launchagent
```

This builds the binary, installs `launchd/be.vhco.postbode.plist` into
`~/Library/LaunchAgents/`, and loads it. The daemon then polls Gmail every
few minutes, stages candidates, and notifies via macOS notification centre
when something needs review.

## CLI surface

The `postbode` binary is a single static executable with subcommands
(invoking it as `postboded` implies `daemon`, for the LaunchAgent's `Label`):

| Command | Purpose |
|---|---|
| `postbode daemon` (or `postboded`) | Run the watcher/uploader loop. |
| `postbode review` | Open the local review UI at `127.0.0.1:7391` in the default browser. |
| `postbode status` | Print queue counts, last poll time, last upload uuid, Gmail token age, items stuck > 48h. |
| `postbode status --find <term>` | "Is this already uploaded?" — one-line verdict by vendor, filename, subject, invoice number or amount. |
| `postbode log [--since <dur>]` | Print the local decision and upload log (e.g. `--since 24h`). |
| `postbode version` | Print the version, commit and Go runtime the binary was built with. |

The review UI is bound to `127.0.0.1` only and requires a session token; it
is never reachable from the LAN.

## Development

```sh
make build          # compile ./bin/postbode (CGO_ENABLED=0)
make test           # go test ./... + golangci-lint run (the standing quality gate)
make test-nonet     # same suite with outbound network blocked in-process (NF-09)
make vet            # go vet ./...
make lint           # golangci-lint run on its own
make e2e-dry        # full pipeline against fixtures/fakes, zero real network calls
```

`make test` runs `check-gitignore` first (fails loudly on any secret pattern
that isn't ignored — this repo has no git remote, so a committed secret
cannot be force-pushed away), then the Go test suite, then `make lint`.
`make lint` fails with a clear message if `golangci-lint` isn't installed,
rather than a confusing error.

No tests contact `api.clearfacts.be` or `gmail.googleapis.com`; all external
contracts are exercised against `httptest` fakes and a fixture `.eml`/`.pdf`
corpus. The only command that touches live systems is `cmd/spike`
(`make spike`), the throwaway P0 validation program deleted once the 14-day
live run (see the plan's Phase 15) confirms the product works.

## Release engineering and versioning

Postbode releases the same way every other `michielvha` Go app does:
GitVersion-driven semver from conventional commits, GoReleaser-built signed
artifacts, `golangci-lint` in the same shape as the source-of-truth
`template-go-app`. See `CLAUDE.md`'s "Release engineering" section for the
full conventional-commit-to-version-bump table.

- Tags are unprefixed (`0.1.4`, not `v0.1.4`), computed by GitVersion
  (`gitversion.yml`) from commit message prefixes on `main`.
- `.goreleaser.yml` builds **`darwin/amd64` and `darwin/arm64` only** —
  never `windows`/`linux`. Postbode shells out to `osascript` and
  `launchctl`, reads the macOS Keychain via `/usr/bin/security`, and
  installs itself as a launchd LaunchAgent; a Linux or Windows binary would
  build cleanly and then fail at first run, so those targets are omitted.
- No container image is built or pushed — there is no meaningful container
  for a macOS desktop daemon that talks to the Keychain and the user's
  notification centre (see the spec's §9, out of scope).
- Release artifacts are zipped, checksummed (`SHA256SUMS`), and the checksum
  file is GPG-signed before the GitHub release is published.
- `.github/workflows/build-and-release.yaml` runs `go vet`, `make test`,
  `make test-nonet` and `golangci-lint` before tagging and releasing —
  CI cannot run yet, since this repo currently has no git remote.
