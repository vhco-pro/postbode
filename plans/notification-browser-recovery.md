# Notification clicks and Gmail recovery — implementation plan

Historical plan. The [feature document](../docs/specs/notification-browser-recovery.spec.md)
now holds the vh-adlc scope, criteria, test plan and lifecycle. The implementation
claims below do not establish a new vh-adlc review or acceptance.

Spec: `docs/specs/notification-browser-recovery.spec.md`.

1. Add deterministic helper resolution and argument construction; use an absolute
   `postbode review` click command and log the selected backend.
2. Add a replaceable, synchronized OAuth token source for the existing Gmail client.
3. Add session-gated consent start and browser-bound callback with PKCE, expiry,
   single use and retry. Persist before publishing the replacement token source.
4. Render persisted auth state and Reconnect Gmail on the review page; replace the
   unusable consent notification with a local recovery instruction, once per episode.
5. Wire production Keychain persistence and the running source; test the real flow
   against fake servers, run repository gates, and write the review summary.

This plan records the repair under the former workflow. Future work follows
vh-adlc through [AGENTS.md](../AGENTS.md).

All five implementation steps are complete. See
`summaries/notification-browser-recovery.md` for executed gates, local activation,
and the notification delivery verification.
