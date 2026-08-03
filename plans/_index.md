# Plan Index

| Plan | Status | Created | Description |
|------|--------|---------|-------------|
| [postbode-gmail-invoice-agent](./postbode-gmail-invoice-agent.md) | not-started | 2026-08-03 | 15-phase build of Postbode, a macOS launchd Go daemon that turns Gmail purchase-invoice attachments into human-approved ClearFacts uploads with four-layer local duplicate prevention (PRD P0 + P1). Spec: [postbode-gmail-invoice-agent.spec.md](../docs/specs/postbode-gmail-invoice-agent.spec.md). |

## Decisions

| ADR | Status | Description |
|-----|--------|-------------|
| [ADR-001](../decisions/ADR-001-four-layer-local-duplicate-prevention.md) | proposed | Four local dedup layers; L3/L4 badge rather than suppress, because the portal cannot be queried. |
| [ADR-002](../decisions/ADR-002-go-native-e2e-without-a-browser-runner.md) | proposed | E2E drives the real localhost listener from Go instead of a browser runner (NF-01/NF-13). |
| [ADR-003](../decisions/ADR-003-upload-delivery-semantics.md) | proposed | At-least-once uploads, claim-in-transaction, and never auto-retrying an unverified upload. |
