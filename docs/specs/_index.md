# Feature document index

These feature documents use vh-adlc YAML frontmatter and required sections,
as declared in [AGENTS.md](../../AGENTS.md). Their paths and legacy identifier
mappings preserve earlier references. The format retrofit leaves them in
`draft`; historical implementation claims do not count as new vh-adlc reviews.

| Spec | Date | Description |
|------|------|-------------|
| [notification-browser-recovery](./notification-browser-recovery.spec.md) | 2026-10-20 | Clickable notifications under launchd and persistent browser-based Gmail recovery. |
| [postbode-gmail-invoice-agent](./postbode-gmail-invoice-agent.spec.md) | 2026-08-03 | macOS launchd Go daemon that polls Gmail, extracts PDF invoices, stages them in a SQLite review queue with a localhost review UI, and on human approval uploads them to the ClearFacts GraphQL API as PURCHASE with four-layer duplicate prevention (PRD phases P0–P1). |
| [resilient-poll-failure-budget](./resilient-poll-failure-budget.spec.md) | 2026-08-25 | Bounds per-message processing failures in the Gmail poll loop with a persisted consecutive-failure budget that parks a persistently failing message loudly and recoverably instead of wedging the daemon, plus consecutive whole-poll failure escalation surfaced by notification, `postbode status` and a new `postbode retry` verb (issues #1 and #2). |
