# Plan Index

| Plan | Status | Created | Description |
|------|--------|---------|-------------|
| [postbode-gmail-invoice-agent](./postbode-gmail-invoice-agent.md) | not-started | 2026-08-03 | 15-phase build of Postbode, a macOS launchd Go daemon that turns Gmail purchase-invoice attachments into human-approved ClearFacts uploads with four-layer local duplicate prevention (PRD P0 + P1). Spec: [postbode-gmail-invoice-agent.spec.md](../docs/specs/postbode-gmail-invoice-agent.spec.md). |
| [resilient-poll-failure-budget](./resilient-poll-failure-budget.md) | not-started | 2026-08-25 | 9-phase build of the per-message failure budget, park-and-continue poll loop, retry admission with a retry-scoped L1 bypass, whole-poll stall escalation, and the `postbode retry` / `postbode status` ops surfaces (issues #1 and #2). Spec: [resilient-poll-failure-budget.spec.md](../docs/specs/resilient-poll-failure-budget.spec.md). |

## Decisions

| ADR | Status | Description |
|-----|--------|-------------|
| [ADR-001](../decisions/ADR-001-four-layer-local-duplicate-prevention.md) | proposed | Four local dedup layers; L3/L4 badge rather than suppress, because the portal cannot be queried. |
| [ADR-002](../decisions/ADR-002-go-native-e2e-without-a-browser-runner.md) | proposed | E2E drives the real localhost listener from Go instead of a browser runner (NF-01/NF-13). |
| [ADR-003](../decisions/ADR-003-upload-delivery-semantics.md) | proposed | At-least-once uploads, claim-in-transaction, and never auto-retrying an unverified upload. |
| [ADR-004](../decisions/ADR-004-park-and-continue-with-bounded-auto-retry.md) | proposed | Park-and-continue in a dedicated `message_failure` table; bounded auto-retry that goes quiet but never ages out. |
| [ADR-005](../decisions/ADR-005-retry-scoped-l1-bypass.md) | proposed | A retry bypasses L1 for exactly one attempt; L2/L3/L4 and F-44 stay in force. |
