---
description: "End-to-end tests drive the real localhost listener over net/http from Go instead of a browser runner, because a JS toolchain would violate the project's no-build-step and restricted-dependency constraints."
status: proposed
date: 2026-08-03
author: "SDD Planner (automated), run by michielvha <<maintainer>>"
---

# ADR-002: Go-native end-to-end testing, no browser runner

## Status

Proposed — accompanies `plans/postbode-gmail-invoice-agent.md` (phase 11).

## Context

Postbode has a genuine user-facing surface: the review UI on `http://127.0.0.1:7391`, through which every approval, rejection and "already in portal" decision passes. G-3 ("nothing is uploaded without human approval") is enforced structurally at that surface, so it deserves real end-to-end coverage rather than handler unit tests.

The default choice for a user-facing surface is Playwright. Three project constraints make it the wrong one here:

- **NF-01** — single static Go binary, no cgo, **no JS build step**, no web framework.
- **NF-13** — dependencies restricted to `google.golang.org/api/gmail/v1`, `golang.org/x/oauth2`, `modernc.org/sqlite`, `github.com/zalando/go-keyring`, plus stdlib.
- **F-42** — the UI is one server-rendered `html/template` page delivered from `embed.FS`, with form POSTs and **no client-side JavaScript at all**.

There is also no CI platform in this project (no constitution, no pipeline, no container). Tests run on one developer's MacBook via `make`. A browser runner would mean a Node toolchain, a `package.json`, a lockfile, a downloaded browser binary and a second language's test conventions — added to a repo whose defining property is that it is one Go binary.

Meanwhile **NF-09** is absolute: no test may contact `api.clearfacts.be` or `gmail.googleapis.com`. Whatever the E2E layer is, it must be provably airtight against live APIs, and that guarantee is easier to enforce inside the Go process than around a browser subprocess.

## Decision

End-to-end tests are **Go-native black-box HTTP**, in `tests/e2e/` behind a `//go:build e2e` tag and driven by `make e2e-dry`.

`tests/e2e/pipeline_test.go` boots the real daemon wiring — real extractor, real rules engine, real SQLite queue, real `webui` listener, real uploader — with only the two external boundaries replaced by `httptest` fakes: the Gmail API and the ClearFacts GraphQL endpoint. It then drives the pipeline exactly as a user would: a fixture mailbox arrives, items stage, the test issues real `POST` requests to `127.0.0.1:7391` with the session token as form fields, and the test asserts on both the HTTP responses and the terminal database state (items `uploaded`, `uuid` and `verified_at` populated, exactly one `messages.modify` recorded by the Gmail fake).

Because the UI ships no JavaScript, a form POST over `net/http` is behaviourally identical to a browser click. Nothing is lost by not rendering pixels.

NF-09 is enforced in-process: `TestMain` installs a dialer that **panics on any dial to a non-loopback address**, and `make test-nonet` runs the whole suite under it. A network-touching test fails loudly instead of leaking (AC-22).

## Alternatives Considered

| Option | Pros | Cons |
|---|---|---|
| **Playwright (`@playwright/test`)** | Archetype default; real browser semantics; screenshots on failure | Requires Node, `package.json`, a lockfile and a browser binary — violates NF-01 (no JS build step) and NF-13 (restricted deps). Enormous overhead for a page with zero client-side JS. No CI exists to amortise it. |
| **`chromedp` (Go, CDP)** | Stays in Go; drives a real browser | Still needs a Chrome binary on the machine; adds a large dependency for no behavioural gain on a JS-free page; makes the NF-09 network guard harder to enforce across the subprocess boundary. |
| **`httptest.Server` only, no real listener** | Simplest | Does not exercise the real bind address, so AC-21 ("listener bound to 127.0.0.1, LAN IP connection refused") becomes untestable. The bind is a security property (NF-04) and must be verified on the real socket. |
| **Handler unit tests only, no E2E** | Least work | Leaves the full pipeline unverified end to end; AC-23 and NF-10 (`make e2e-dry`) are explicit requirements. Also removes the regression net that phase 11 exists to provide before the high-risk L3/L4 work lands. |
| **Manual click-through as the E2E layer** | Zero code | Not repeatable, not a gate, and worthless as a regression net for phases 12–14. |

## Consequences

**Positive.** The whole test suite is one `go test` invocation with no second toolchain, no lockfile and no browser download. NF-09 becomes self-enforcing rather than aspirational, because the guard lives in the same process as the code under test. `make e2e-dry` lands in phase 11, **before** the highest-risk work (L3 identity-key parsing), giving that work a full-path regression net.

**Negative.** No coverage of browser-rendered behaviour: CSS layout, template rendering artifacts, and anything a future contributor might add in client-side JS would be invisible to these tests. That is acceptable precisely because F-42 forbids client-side JS — **if that ever changes, this ADR must be revisited**, because its central premise (a form POST is equivalent to a click) stops being true.

**Constraint recorded.** AC-21's LAN-IP assertion is environment-dependent — a machine with no non-loopback interface makes it vacuous. The test asserts the listener's bind address directly **and** attempts the LAN-IP dial only when a non-loopback interface exists, skipping with a logged reason otherwise (plan OQ-P13).
