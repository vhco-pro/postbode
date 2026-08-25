---
description: "Per-message failure budget, park-and-continue poll loop, bounded retry with a retry-scoped L1 bypass, and whole-poll stall escalation — all 14 acceptance criteria verified against executed tests and executed gates."
status: complete
reviewed: 2026-08-25
reviewer: "SDD Reviewer (automated), run by michielvha <michielvh@outlook.com>"
plan: plans/resilient-poll-failure-budget.md
---

# Resilient poll — per-message failure budget and stall escalation, Implementation Summary

## Overview

The branch `feature/resilient-poll-failure-budget` (single commit `7d46258`, base `main` at `0b99895`) makes it structurally impossible for one message, or one error shape, to stall the mailbox silently. A per-`gmail_message_id` consecutive-failure count is persisted in a new `message_failure` table; under budget the poll still aborts and retries next tick (behaviour deliberately unchanged), and at budget the message is *parked* — notified once, logged, reported by `postbode status`, never aged out — so the loop continues and `SaveSyncState` is reached. Parked messages re-enter the poll only by id, through a bounded automatic retry schedule or `postbode retry`, and an admitted retry bypasses the L1 message-id skip for exactly that one attempt (ADR-005) so a message parked after `RecordMessageIfNew` is genuinely re-extracted rather than silently skipped forever. Separately, every poll cycle that ends without persisting `sync_state` is counted, and three in a row announce themselves once per episode.

All four gates were executed by this review, not taken on report: `make test`, `go vet ./...`, `make test-nonet` (forced uncached with `-count=1`) and `make lint` are green, 324 top-level tests pass with 0 failures and 0 skips, and all ten `TestE2EDry_*` scenarios pass including the three new ones.

## What Was Implemented

### Persistence (`internal/queue`)
- Migration version 4 — `message_failure` table plus `idx_message_failure_next_retry_at`, with **no foreign key to `message`**, see `internal/queue/schema.go:159-186`
- Migration version 5 — four additive `ALTER TABLE sync_state ADD COLUMN` statements, see `internal/queue/schema.go:187-204`
- Per-message CRUD, park/re-park, due-retry admission, unpark and the bounded backoff, see `internal/queue/message_failure.go:33-315`
- Whole-poll stall counter with once-per-episode escalation, see `internal/queue/poll_failure.go:25-67`
- `queue.DB.FailedIDs`, one indexed read per poll cycle, see `internal/queue/message_failure.go:180-199`

### Poll loop (`internal/gmailwatch`)
- `consumesBudget` as a sibling predicate of `isReauthError` / `isMessageGone`, see `internal/gmailwatch/budget.go`
- Classify / record / park / `continue`, replacing the pre-`SaveSyncState` return, see `internal/gmailwatch/poll.go:123-208`
- Retry admission by id with the per-cycle `admitted` set, see `internal/gmailwatch/poll.go:74-117`
- `failPoll`, the single non-progressing exit path, see `internal/gmailwatch/poll.go:253-300`
- F-76's effective budget of 1 for any message with `park_count >= 1`, see `internal/gmailwatch/poll.go:314-327`
- `Watcher.Clock` and the four config accessors, see `internal/gmailwatch/watcher.go:62-93, 133`

### Extraction (`internal/extract`)
- `Message.ForceReextract`, documented at its declaration with the ADR-005 pointer, see `internal/extract/extract.go:27-50`
- Both L1 return paths guarded — the early `MessageSeen` skip and the `alreadySeen` race branch, see `internal/extract/extract.go:168-176` and `:224-237`

### Configuration, ops surfaces and docs
- Four `gmail:` keys with defaults 3 / 6h / 3 / 3, allow-list entries and `> 0` validation carrying the offending line number, see `internal/config/config.go:78-94, 136-140, 365-369, 405-458`
- `poll health:` and `parked messages:` sections, see `internal/cli/status.go:170-193, 220-242`
- `postbode retry <id>` / `--all`, see `internal/cli/retry.go` and `cmd/postbode/retry.go`
- `notify.ParkedMessage` / `notify.PollStalled`, see `internal/notify/messages.go:41-88`
- README config section and the CLAUDE.md parked-message policy note

## Verification

Gate output captured this session:

```
make test        → check-gitignore ok; 21 packages ok; golangci-lint: 0 issues
go vet ./...     → exit 0, no output
make test-nonet  → POSTBODE_TEST_NO_NETWORK=1 go test -count=1 ./... → EXIT=0, 21 packages ok
make lint        → golangci-lint run → 0 issues
go test ./... -count=1 -v → 324 PASS / 0 FAIL / 0 SKIP
go test ./tests/e2e/... -run TestE2EDry -v → 10/10 PASS (7 pre-existing + 3 new)
```

| Criterion | Result | Evidence |
|-----------|--------|----------|
| AC-34: three-poll park, `history_id` advances, `C` still staged, fourth poll quiet | PASS | `TestPollParksAtBudgetAndDrainsTheRest` in `internal/gmailwatch/park_test.go:109-184` asserts polls 1–2 error with `history_id == "1"`, poll 3 parks `msg-b` with `history_id == "4242"`, A and C staged, `failure_count == 3`, poll 4 stages/parks/notifies nothing. **E2E**: `TestE2EDry_PoisonMessageParksAndMailboxDrains` in `tests/e2e/resilience_test.go:33-109` proves the same through the real watcher/extractor/rules/queue/webui wiring. Both executed 2026-08-25 |
| AC-35: fail twice then succeed, never parked, row gone | PASS | `TestTransientFailureSelfHealsAndNeverParks` in `internal/gmailwatch/park_test.go:187-231`; `TestClearMessageFailureResetsTheCount` in `internal/queue/message_failure_test.go:85`. Executed |
| AC-36: 404 skipped on poll 1, no budget, no park, no notify, `sync_state` persisted; both pre-existing tests unmodified | PASS | `TestGoneMessageConsumesNoBudget` (`park_test.go:283`), `TestGoneMessageClearsAnExistingPark` (`park_test.go:321`), `TestConsumesBudget` (`budget_internal_test.go:15`). `git diff main...7d46258 -- internal/gmailwatch/deleted_message_test.go` is **empty**, and `TestPollSkipsDeletedMessageAndKeepsGoing` + `TestPollStillFailsOnNonNotFoundMessageError` were executed unmodified and pass. `internal/gmailwatch/gone.go` is also unmodified |
| AC-37: cancelled mid-loop, no row, next poll normal | PASS | `TestCancelledPollConsumesNoBudget` (`park_test.go:359`), `TestCancelledPollIsNotCountedAsAStall` (`stall_test.go:337`), `TestConsumesBudget` table covers `context.Canceled`, `DeadlineExceeded` and wrapped forms. Executed |
| AC-38: `invalid_grant` mid-loop is byte-for-byte AC-20, no `message_failure` row | PASS | `TestPollSurvivesInvalidGrantAndResumesWithoutRestart` in `internal/gmailwatch/reauth_test.go:18`, extended at `:90-101` with `ListParkedMessages` and failure-row assertions. Executed |
| AC-39: exactly one park notification, zero on re-encounter | PASS | `TestParkNotifiesExactlyOnce` (`park_test.go:234-280`) asserts the id, the "NOT in the review queue" wording and the `postbode retry msg-poison` command, then zero further notifications across polls 2–3; `TestRecordMessageFailureReportsParkOnlyOnce` (`message_failure_test.go:66`). Notifier is `notify.Fake`; `osascript` is never executed. Executed |
| AC-40: `parked messages:` section, and `parked messages:  0` when empty | PASS | `TestStatusReportsParkedMessages`, `TestStatusPrintsAnExplicitZeroForParkedMessages`, `TestStatusNamesTheRetryCommandForAnExhaustedMessage` in `internal/cli/status_parked_test.go:26, 59, 68`, all against `cli.FormatStatus` with an injected `now`. Executed |
| AC-41: `retry <id>` reaches a message no listing returns; `--all`; exit 2; unknown id; concurrent writer | PASS (one clause deviates, see Finding 2) | `TestRetryUnparksOneMessage`, `TestRetryAllUnparksEverything`, `TestRetryAllWithNothingParkedIsNotAnError`, `TestRetryUsageErrors`, `TestRetryWritesWhileASecondConnectionIsOpen` (`internal/cli/retry_test.go:27, 64, 93, 104, 162`); `TestRetryExitCodes` / `TestRetryAppearsInUsage` (`cmd/postbode/retry_test.go:14, 73`); `TestManualRetryReachesAMessageNoListingReturns` (`internal/gmailwatch/stall_test.go:249`) drives `HistoryFunc` to return nothing and still stages. **E2E**: `TestE2EDry_ParkedMessageRetriedManuallyReachesQueue` (`tests/e2e/resilience_test.go:112`). Executed |
| AC-42: two auto retries then exhausted; re-park never aborts; healed message succeeds on first retry | PASS | `TestAutomaticRetryIsBoundedAndNeverRewedgesThePoll` (`stall_test.go:139-205`) asserts `res.Retried` per cycle, `history_id == "4242"` **and** `st.PollHealthy()` on every retry cycle, then no third retry and `NextRetryAt == nil` while still listed; `TestAutomaticRetryRecoversAHealedMessage` (`stall_test.go:208`); `TestRepeatedlyParkedMessageGetsAnEffectiveBudgetOfOne` (`stall_test.go:296`); `TestRecordRetryAttemptBoundsAutomaticRetries` and `TestRetryBackoffIsCappedAtADay` (`message_failure_test.go:184, 239`). Executed |
| AC-43: 0/0/1/0×7 stall notifications, status wording, reset, new episode, park-only suppression | PASS (coverage gap, see Finding 1) | `TestWholePollStallEscalatesOncePerEpisode` (`stall_test.go:24`), `TestParkCycleSuppressesTheStallNotification` (`stall_test.go:106`), `TestRecordPollFailureEscalatesExactlyOncePerEpisode` / `TestClearPollFailureEndsTheEpisodeSoALaterStallNotifiesAgain` / `TestRecordPollFailureDoesNotAdvanceProgressFields` (`poll_failure_test.go:11, 47, 130`), `TestStatusStatesPollHealthInWords` (`status_parked_test.go:90`). **E2E**: `TestE2EDry_StalledDaemonAnnouncesItselfAndSaysSoInStatus` (`tests/e2e/resilience_test.go:187`). Executed |
| AC-44: state survives close/reopen; no re-notify; no counter reset; prune leaves parks intact | PASS | `TestParkedStateSurvivesReopen` (`message_failure_test.go:269`), `TestPollStallStateSurvivesReopen` (`poll_failure_test.go:90`), `TestParkedMessagesAreImmuneToRetention` + `TestParkedMessageNeverBecomesAQueueItem` (`park_retention_test.go:19, 55`). Independently confirmed by grep: the only `DELETE FROM message_failure` in the tree is `ClearMessageFailure` at `internal/queue/message_failure.go:120`. Executed |
| AC-45 — the silent-miss guard: re-extracted not L1-skipped; no second uploadable item; rejection stays rejected | PASS | `TestForceReextractRecoversAMessageStuckBehindL1` (`force_reextract_test.go:53`, asserts a *plain* retry is still skipped before asserting the forced one is not, so the test cannot go vacuous), `TestForceReextractStillHonoursL2` (`:101`, uploadable == 3 / `duplicate_linked` == 3 via `DedupLayerL2`), `TestForceReextractStillHonoursRejectionMemory` (`:155`), `TestBypassDoesNotLeakToOrdinaryMessages` (`:197`, three F-13 resyncs still L1-skip), `TestForceReextractKeepsTheSpoolFailSafe` (`:231`). **E2E**: `TestE2EDry_ParkedMessageRetriedManuallyReachesQueue`. Executed |
| AC-46: `make test && go vet ./...` green; suite green under the non-loopback dialer guard; no live endpoint referenced | PASS | All four gates executed above; `make test-nonet` re-run with `-count=1` to defeat the build cache, EXIT=0. `grep -rn "gmail.googleapis.com\|api.clearfacts.be" --include='*_test.go' .` returns three hits, all prose comments, zero call sites |
| AC-47: four keys load; `failure_budget: 0` / `park_retry_attempts: -1` rejected with a line number; defaults 3/6h/3/3 | PASS | `TestResilienceKeysLoad`, `TestResilienceKeysDefaultWhenOmitted`, `TestResilienceKeysRejectNonPositiveValuesWithLineNumbers`, `TestResilienceKeysAreOnTheAllowList` in `internal/config/resilience_keys_test.go:23, 53, 89, 139`. Executed |

### Adversarial probes performed beyond the criteria

**The L1 bypass cannot leak (AC-45 / F-78 / ADR-005).** `ForceReextract` has exactly one production assignment in the whole tree — `internal/gmailwatch/poll.go:402` — and its value is the `forceReextract` *parameter* of `processMessage`, sourced only from `admitted[id]`, a map built solely from `w.DB.DueRetries` at `poll.go:83-94`. It is threaded as an argument rather than stored on the `Watcher`, so there is no state to forget to clear and a listed id gets `false` by map default. Both `Skipped` return paths in `ExtractMessage` are guarded (`extract.go:168` and `:224`), so a retried message cannot be silently L1-skipped; `TestForceReextractRecoversAMessageStuckBehindL1` asserts `!forced.Skipped` directly. A second uploadable item is impossible because the bypass ends before `StageItem`, which still applies L2/L3/L4 and F-44 unchanged — `internal/dedup/`, `internal/rules/` and `internal/uploader/` have **zero diff** on this branch.

**The retry schedule is genuinely bounded.** Deviation 1's fix is complete: `RecordRetryAttempt` (`message_failure.go:277-293`) is the sole writer of `next_retry_at` after the initial arming, and the re-park branch in `RecordMessageFailure` (`:69-81`) increments `ParkCount` only. Exhaustion sets `next_retry_at` to NULL and `DueRetries` requires `next_retry_at IS NOT NULL`, so a permanently broken message stops generating automatic work. The attempt is spent *before* processing (`poll.go:107-116`), so a crash mid-cycle cannot loop faster than the schedule. `backoff` (`:303-315`) returns the 24h cap before any doubling that could overflow, so no wrap is reachable for any positive base. `TestAutomaticRetryIsBoundedAndNeverRewedgesThePoll` is the regression guard that originally caught the double-writer bug.

**Migration safety verified empirically, not by inspection.** `git diff` shows `internal/queue/schema.go` as `+46 -0` at the end of the file, so versions 1–3 are untouched, and `TestShippedMigrationsAreFrozen` pins their SHA-256. This review additionally *built* a real schema-version-3 database (versions 1,2,3 in `schema_migrations`, no `message_failure`, `sync_state` with only the v1/v2 columns) and ran the real `queue.Open` against it: it migrated to `applied versions: 1 2 3 4 5`, preserved pre-existing `sync_state` data (`history_id=9999`, `label_id_submitted=Label_42`), added the four columns additively, and `RecordMessageFailure` / `ListParkedMessages` / `RecordPollFailure` all worked immediately afterwards. `PRAGMA foreign_key_list(message_failure)` returns **empty**, confirming the spec §5.2 / ADR-004 no-FK hard constraint.

**CLAUDE.md compliance.** No test contacts a live endpoint (grep + a forced uncached `make test-nonet` pass). `make check-gitignore` is green and no credential, token, `*.db` or `spool/` path appears in the diff. L3/L4 still never auto-suppress — those packages are unmodified. No auto-approve path exists; the only two `auto-approve` string matches in non-test code are comments asserting its absence.

**Conventional commit.** `feat(gmailwatch): bound per-message failures and escalate a stalled poll` — `feat:` maps to a **minor** bump, which is what the plan's Release Engineering section requires for every phase that ships a surface. No `!` and no `BREAKING CHANGE` in the body, correct for a change that is additive throughout (new table, new columns with defaults, new keys with defaults, new verb, new `PollResult` fields).

## Files Changed

41 files, +5206 / −43. Beyond the surfaces listed above: `cmd/postbode/main.go` and `cmd/postbode/daemon.go` wiring, `.github/workflows/build-and-release.yaml`, `docs/specs/resilient-poll-failure-budget.spec.md`, `decisions/ADR-004-*.md`, `decisions/ADR-005-*.md`, `plans/_index.md`, `docs/specs/_index.md`, `vega.yaml`, and 15 new or extended test files.

## Outstanding Issues

**Finding 1 (Should) — the F-83 suppression and notified-marker rollback branch is never executed by any test.** `go tool cover` on `internal/gmailwatch` reports execution count **0** for `poll.go:280.25→285.20`, the block that rolls `PollStallNotifiedAt` back to `nil` when a cycle both parked something and then aborted under budget. `TestParkCycleSuppressesTheStallNotification` verifies AC-43's observable clause, but its parking cycle *succeeds* and therefore reaches `ClearPollFailure` — it never enters `failPoll` at all, so it exercises the success path, not the suppression path. This matters because the Implementation Notes explicitly flag the rollback for the reviewer's eye, and it is load-bearing: without it, a cycle that parks one message and then aborts under budget on another would consume the episode's one notification without sending it, and the genuine ongoing stall would stay silent until a successful poll. Traced by hand, the logic is **correct** — the rollback restores `PollStallNotifiedAt = nil` while `ConsecutivePollFailures` keeps climbing, so the next non-parking failed cycle re-escalates and notifies. The defect is absent; the *test* for it is absent. Recommend a follow-up test with two failing messages at different failure counts (A parks on cycle 3, B aborts under budget in the same cycle) asserting zero notifications on that cycle and exactly one on the next. Recommend opening an issue.

**Finding 2 (Should) — AC-41's literal "clears `parked_at`" clause is not satisfied, and cannot be.** `queue.Unpark` (`internal/queue/message_failure.go:240-253`) sets `next_retry_at = now` and deliberately leaves `parked_at` set. This is a contradiction inside the plan itself, not an implementation defect: the plan's own retry-admission design gives `DueRetries` the predicate `WHERE parked_at IS NOT NULL AND next_retry_at IS NOT NULL`, so clearing `parked_at` would make the retried message un-admittable — and would also drop it out of `ListParkedMessages` before it had actually succeeded, which F-79 forbids. The implementation resolved the contradiction the safe way and documented why at the function. The two `// Verifies:` comments at `internal/cli/retry_test.go:26` and `internal/gmailwatch/stall_test.go:248` still quote the stale clause, though the assertions underneath test the correct behaviour. Recommend correcting AC-41's wording in the plan and those two comments to "makes the message due for the next poll (`next_retry_at`), keeping it parked and reported until it succeeds". This was not recorded as a third deviation in the Implementation Notes and should have been.

**Finding 3 (Should) — the plan-declared v3→v5 forward-migration test was never written.** Phase 1's exit criteria says "A fresh database and a database created at schema version 3 both migrate to 5", and Test Plan Part B's NF-15 row says "a v3 database migrates cleanly to v5" in `internal/queue/schema_test.go` (extended). Neither `schema_test.go` nor `migration_test.go` was modified on this branch (`git diff` empty for both), and no test anywhere constructs a database at version 3. The freeze half of that row *was* delivered, in the new file `internal/queue/migration_freeze_internal_test.go`. `TestMigrationRunnerIsIdempotentAndPreservesData` covers reopen-at-current-version, which is a different property. This review closed the gap manually (see the migration probe above, which passed), so the behaviour is confirmed correct — but there is no standing regression guard, and the next migration will not have a reviewer building a synthetic old database by hand. Recommend adding it.

**Finding 4 (Consider) — one non-progressing exit path neither consumes budget nor parks.** `Poll` returns at `internal/gmailwatch/poll.go:26-29` when the opening `GetSyncState` fails, without calling `failPoll`. Every other return-before-`SaveSyncState` path is covered: the two deliberate exclusions are `handleReauth` (loud via F-16's own once-per-episode notification) and context cancellation (`poll.go:160`, daemon shutdown), and the remaining ten all route through `failPoll`. NF-14's structural claim therefore holds for every error shape except a failure to read `sync_state` itself. The practical impact is small and partly self-limiting — `RecordPollFailure` begins with its own `GetSyncState`, so a persistently unreadable database could not record the stall anyway — but a transient `SQLITE_BUSY` past the 5s timeout would go uncounted. Worth a one-line `failPoll` call for completeness.

**Notes acknowledged, no disagreement:** OQ-P70's resolution via `queue.DB.FailedIDs` is correct and better than the plan's in-memory suggestion — the map would indeed be empty after a restart while the rows persisted. ADR-004/ADR-005 remaining at `status: proposed` matches ADR-001…003.

**Test-file location deviations (informational only):** several tests landed in differently named files than the Test Plan specified — `budget_internal_test.go` (not `budget_test.go`), `stall_test.go` (absorbing both `poll_stall_test.go` and `park_retry_test.go`, and the `retry_admission_test.go` case), `resilience_keys_test.go` (not an extension of `config_test.go`), plus new `park_retention_test.go`, `migration_freeze_internal_test.go` and `webui/parked_absent_test.go`. Every Test Plan row has a real, executed, passing test behind it; only the filenames moved. Every new test carries a `// Verifies:` traceability comment quoting its criterion — no omissions found.

## Recommendation

**Status transition**: `review` → `complete`

All fourteen acceptance criteria AC-34…AC-47 verify against tests that were executed this session and that assert what the criteria actually say; all four gates pass, the three E2E scenarios pass, the two frozen `deleted_message_test.go` guards pass unmodified, and the four sharpest hazards (L1 bypass leakage, unbounded retry, migration safety, no-FK) were probed adversarially and hold. The four findings are test-coverage and plan-wording gaps rather than defects in shipped behaviour, each verified correct by hand or by an out-of-tree probe during this review; none of them blocks the merge, and Findings 1–3 are worth follow-up issues before the next change to this subsystem.
