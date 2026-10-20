# Notification clicks and Gmail recovery — review

Date: 2026-10-20. Self-review of the local working tree based on `e519868`.
Spec: `docs/specs/notification-browser-recovery.spec.md`.
Plan: `plans/notification-browser-recovery.md`.

## Findings and repair

The running Homebrew LaunchAgent had PATH `/usr/bin:/bin:/usr/sbin:/sbin`
(`launchctl print gui/501/homebrew.mxcl.postbode`, executed). Helper lookup
therefore missed `/opt/homebrew/bin/terminal-notifier` and used AppleScript,
which supplied no browser click action. This explains the missing action; the
reported Finder activation itself was not reproduced interactively.

Helper discovery now checks both Homebrew prefixes after PATH. Clicks execute
the absolute current binary's `review` command so a fresh browser session works,
without storing a session token in Notification Center. The selected backend is
logged. The fallback logs an explicit instruction to install the helper.

Re-auth previously embedded an unusable consent URL in text and had no callback
to save and install its result. The review page now reads persisted auth health
and always offers Reconnect Gmail when OAuth is configured. Consent uses PKCE
and random, browser-bound, expiring, single-use state. Saving in Keychain precedes
publication to the running Gmail client's synchronized token source. Subsequent
polls clear auth health normally; no restart or queue decision change is needed.
Repeated failures notify once per persisted episode.

## Acceptance review

| Spec criterion | Evidence | Result |
|---|---|---|
| N1: launchd helper discovery | `TestHelperDiscoveryWithMinimalPath`; restarted daemon log confirms `/opt/homebrew/bin/terminal-notifier` | PASS |
| N2: authenticated browser action | `TestClickRunsAbsoluteReviewWithoutNotificationSecrets` captures real helper arguments and executes the shell command using a harmless path containing spaces, quotes and shell syntax | PASS in isolation; macOS banner click pending |
| N3: durable recovery and no repeated alerts | `TestConsentRepairsRunningGmailClient` checks two invalid_grant polls, one notification, persisted state, and recovery on an empty queue | PASS |
| N4: bound consent with retry | `TestConsentRejectsUnboundExpiredAndSupersededRequests`; same test checks missing review session, invalid/missing browser binding, expiry, supersession, denial and retry; successful test checks PKCE and replay | PASS |
| N5: persist, then repair existing client | `TestConsentRepairsRunningGmailClient` drives token exchange and a subsequent poll through the same service; `TestInstallGmailTokenPersistsBeforeReplacingLiveSource` checks production wiring with failed and successful fake storage; `TestConsentCanReplaceSourceDuringBlockedRefresh` checks recovery during old network I/O | PASS |
| N6: recoverable failures without secret disclosure | `TestConsentExchangeAndStorageFailuresAreRecoverable` covers exchange, missing refresh token and storage failure; denial input is never echoed; exchange context is bounded to 30 seconds and Keychain to 20 seconds | PASS |

Executed checks:

- `make test`: all packages pass; secret-ignore check passes; lint reports 0 issues.
- `go vet ./...`: passes.
- `make test-nonet`: passes. An additional uncached
  `POSTBODE_TEST_NO_NETWORK=1 go test -count=1 ./...` also passed.
- `go test -race ./internal/gmailwatch ./internal/webui ./internal/notify`: passes.
- `make build` and `git diff --check`: pass.

No automated test used the real Keychain, posted desktop notifications, or called
live Google/ClearFacts APIs. Protocol behavior was checked against the
[helper documentation](https://github.com/julienXX/terminal-notifier/blob/3.1.0/README.markdown#click-actions)
and [Google desktop OAuth documentation](https://developers.google.com/identity/protocols/oauth2/native-app).

## Local activation and remaining check

Installed the built binary into the existing local Homebrew service and restarted
that service. The original binary is preserved at
`bin/postbode-before-notification-recovery`; the resolved destination is recorded
in `bin/notification-recovery-install.json`. Both are ignored build artifacts.
Activation used automatic rollback on failure; rollback was not needed.
An authenticated HTTP request confirmed the new reconnect form, and the installed
binary opened the page in the default browser. The daemon log confirmed selection
of the clickable helper. This is a local repair; no release or source commit was
published, and a Homebrew reinstall can replace the local binary.

The initial `terminal-notifier -diagnose` reported authorization denied. On the
follow-up, enabled Allow notifications in System Settings directly and verified
the switch read on. The helper now reports authorization **authorized** and
**No problems found**. Posted a Postbode test notification with the review click
command and confirmed its delivery with `-list postbode-recovery-check`.
Removed that temporary notification after the check and reopened the review UI.
No user settings action remains outstanding.

Clicking the actual macOS notification overlay could not be verified: the
computer-use bridge timed out accessing NotificationCenter and ControlCenter.
The underlying command was already executed successfully by the isolated
regression check. A temporary keyboard-shortcut experiment was restored to its
original disabled/none state and verified in System Settings. No live Google
consent was attempted; the daemon did not currently report a re-auth condition.
