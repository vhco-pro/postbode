# IMAP + app-password escape hatch (documented, not implemented)

Spec trace: **F-18**, PRD §6.1. **There is zero code for this.** It exists as a written fallback so
the decision is recorded rather than rediscovered under pressure.

## When you would reach for it

Postbode uses the Gmail API with an OAuth desktop client. The known friction is that
`gmail.readonly` and `gmail.modify` are *restricted* scopes, so a consent screen left in **Testing**
status issues refresh tokens that expire every **7 days**
([Google](https://developers.google.com/identity/protocols/oauth2)). Postbode treats re-auth as a
routine event (F-16) rather than a failure, so this is survivable — but if the weekly consent dance
becomes intolerable and publishing to Production doesn't resolve it, IMAP is the exit.

## What it would look like

1. Enable 2FA on the Google account, mint an **app password**.
2. Connect to `imap.gmail.com:993` with IMAP IDLE instead of 5-minute polling.
3. Store the app password in the macOS Keychain exactly as the PAT is stored.

## What you would lose — read this before switching

| Loss | Consequence |
|---|---|
| **No label writes** | The `vh&co/submitted` move (F-14) is gone. Processed mail stays in the inbox and *all* "have I handled this" state lives in SQLite. L1 becomes the only thing standing between you and reprocessing the entire mailbox. |
| **Broader access** | An app password grants **full mailbox access**, not the read-plus-label-write that the OAuth scopes are limited to (NF-02). This is strictly worse for a tool that reads personal mail. |
| **No history sync** | `history.list` incremental sync (F-12) has no IMAP equivalent. You fall back to UID-range scanning, and the crash/gap semantics F-13 handles get re-derived from scratch. |

## Verdict

Keep it as a documented escape hatch, not a supported mode. The OAuth path's cost is a periodic
click; the IMAP path's cost is a permanent widening of access plus losing the label channel that
makes "already submitted" visible in Gmail itself.
