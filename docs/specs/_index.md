# Spec Index

| Spec | Date | Description |
|------|------|-------------|
| [postbode-gmail-invoice-agent](./postbode-gmail-invoice-agent.spec.md) | 2026-08-03 | macOS launchd Go daemon that polls Gmail, extracts PDF invoices, stages them in a SQLite review queue with a localhost review UI, and on human approval uploads them to the ClearFacts GraphQL API as PURCHASE with four-layer duplicate prevention (PRD phases P0–P1). |
