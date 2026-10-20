---
title: "Notification clicks and Gmail recovery"
type: feature
slug: notification-browser-recovery
status: draft
status_description: "Retrofitted to vh-adlc format; historical implementation claims are preserved, and vh-adlc review and acceptance are not recorded."
issue: null
goal: "Open Postbode notifications in the authenticated browser UI and keep Gmail reconnection available after notification dismissal."
created: "2026-10-20"
version: "1.0"
legacy_status: "Implemented; notification permission enabled and delivery verified."
covers:
  - internal/notify/
  - internal/gmailwatch/
  - internal/webui/
  - internal/keychain/
  - cmd/postbode/
legacy_criterion_ids:
  "N1": AC-50
  "N2": AC-51
  "N3": AC-52
  "N4": AC-53
  "N5": AC-54
  "N6": AC-55
---

# Notification clicks and Gmail recovery

## Context

The click action is implemented in `internal/notify/notify.go:83`; consent starts and returns through `internal/webui/gmail_auth.go:32` and `internal/webui/gmail_auth.go:61`. The incident report below describes the original failure. The prior implementation status is recorded in `legacy_status`; review records are not inferred from test output.

- `internal/notify/notify.go`: helper discovery used only PATH. Executed
  `launchctl print gui/501/homebrew.mxcl.postbode` confirms the running daemon
  has PATH `/usr/bin:/bin:/usr/sbin:/sbin`, excluding the installed helper at
  `/opt/homebrew/bin/terminal-notifier`. It therefore selected AppleScript.
- AppleScript notifications have no click handler. The reported Finder activation
  has not yet been reproduced interactively; the missing handler is confirmed in code.
- `internal/gmailwatch/reauth.go`: consent URL was embedded in text, used fixed state,
  and had no callback or token installation. Repeated failures notified every poll.
- `internal/webui/handlers.go`: persisted `LastAuthError` was absent from the page.
- Notification review URLs assumed the browser already had a session cookie.

## Scope

Complete F-16 and F-45 recovery: helper discovery, authenticated notification clicks, durable re-auth visibility and browser consent. First-time bootstrap without a cached Gmail identity stays outside this repair.

## Design

`BestReview` in `internal/notify/notify.go:83` runs the current binary's review command. The handlers in `internal/webui/gmail_auth.go:32` and `internal/webui/gmail_auth.go:61` bind consent to the browser and install its result. `internal/gmailwatch/token_source.go:13` supports replacing the running client's token source after persistence.

The server remains loopback-only. Callback redirect URI is fixed by application
configuration, never derived from request Host. Every upload still needs approval;
parked mail and all four duplicate prevention layers are unchanged. No live API
calls or Keychain access are used by tests. First-time bootstrap without a cached
Gmail identity remains outside this bug fix.

## Acceptance Criteria

The `legacy_criterion_ids` mapping preserves the original N1 through N6 references.

- [ ] AC-50: Homebrew helpers are found with launchd's minimal PATH on ARM and Intel; fallback remains available and is logged.
- [ ] AC-51: Review notification clicks run the absolute current binary's `review` command, obtaining the current session token without storing it in Notification Center.
- [ ] AC-52: Re-auth failures notify once per episode with a recoverable local review URL; persisted auth state appears even with an empty review queue.
- [ ] AC-53: The page always offers Reconnect Gmail when configured; it works after dismissal or daemon restart. Start requires the review session token; consent uses random, expiring, browser-bound, single-use state and PKCE.
- [ ] AC-54: Successful consent exchanges the code, persists the token in Keychain, then atomically replaces the running client's token source. Normal polling clears the auth error, without restarting or touching queue decisions.
- [ ] AC-55: Failed/denied consent offers a retry. Token exchange is bounded; no secrets/errors from the provider are echoed into HTML.

## Test Plan

| Criterion | Proof |
| --- | --- |
| AC-50 | `TestHelperDiscoveryWithMinimalPath` in `internal/notify/click_test.go` checks both Homebrew prefixes and absence; inspect daemon backend logging for fallback. |
| AC-51 | `TestClickRunsAbsoluteReviewWithoutNotificationSecrets` in `internal/notify/click_test.go` captures helper arguments and executes the quoted command against a harmless executable. The macOS overlay click remains unverified in the historical summary. |
| AC-52 | `TestConsentRepairsRunningGmailClient` in `internal/webui/gmail_auth_test.go` checks repeated invalid_grant polls, one notification, persisted auth state and the empty-queue recovery action. |
| AC-53 | `TestConsentRejectsUnboundExpiredAndSupersededRequests` and `TestConsentRepairsRunningGmailClient` in `internal/webui/gmail_auth_test.go` check session/state/cookie binding, PKCE, denial, expiry, supersession, retry and replay; inspect startup wiring for restart availability. |
| AC-54 | `TestConsentRepairsRunningGmailClient` in `internal/webui/gmail_auth_test.go`, `TestInstallGmailTokenPersistsBeforeReplacingLiveSource` in `cmd/postbode/gmail_install_test.go`, and `internal/gmailwatch/token_source_test.go` check persistence ordering and recovery of the same service. |
| AC-55 | `TestConsentExchangeAndStorageFailuresAreRecoverable` in `internal/webui/gmail_auth_test.go` checks exchange, missing-refresh-token and storage errors; inspect the bounded exchange context in `internal/webui/gmail_auth.go`. |

## Tasks

None for this documentation retrofit. Prior implementation phases remain in the [historical plan](../../plans/notification-browser-recovery.md) and [implementation summary](../../summaries/notification-browser-recovery.md).

## Open Questions

None.

## Review Log

| Date | Review | Kind | Commit | Model | Isolation | Outcome |
| --- | --- | --- | --- | --- | --- | --- |

review pending: No independent vh-adlc spec review has run for this converted document. Earlier implementation claims remain in the linked historical records and do not count as vh-adlc acceptance.

## Sources

- [Feature-document standard](https://github.com/vhco-pro/docs/blob/e4d58d2cd69bd42f1efacf91dfdd3f01a24e134a/conventions/feature-documents.md): frontmatter, sections, criteria and review semantics.
- [Feature-document template](https://github.com/vhco-pro/docs/blob/e4d58d2cd69bd42f1efacf91dfdd3f01a24e134a/guides/feature-document-template.md): document structure.
- [Prose guide](https://github.com/vhco-pro/docs/blob/e4d58d2cd69bd42f1efacf91dfdd3f01a24e134a/guides/humanize-prose.md): wording of the retrofit.
- [Documentation conventions](https://github.com/vhco-pro/docs/blob/e4d58d2cd69bd42f1efacf91dfdd3f01a24e134a/conventions/documentation.md): preservation of authoritative records and plain wording.
- [Delivery conventions](https://github.com/vhco-pro/docs/blob/e4d58d2cd69bd42f1efacf91dfdd3f01a24e134a/conventions/delivery.md): documentation commits and separation from implementation delivery.
- [terminal-notifier 3.1.0](https://github.com/julienXX/terminal-notifier/blob/3.1.0/README.markdown#click-actions): protocol reference from the original implementation, retained without a new vendor audit.
- [Google desktop OAuth](https://developers.google.com/identity/protocols/oauth2/native-app): protocol reference from the original implementation, retained without a new vendor audit.
