// Package webui implements Postbode's local review UI (spec §6.3, F-40…
// F-47): the single embedded page through which the human, and only the
// human, approves, rejects or teaches-once every staged item. It is a
// plain Go html/template + embed.FS server — no JS build step, no
// framework, no client-side JS at all (NF-01) — bound exclusively to
// 127.0.0.1:7391 (F-42, NF-04) and gated by a random per-daemon-start
// session token on every mutating request (F-46).
//
// # Scope
//
// This package owns the UI's HTTP surface and its session-token handoff
// file. It does not own uploading (Phase 9), the L3/L4 dedup matching
// engine that sets an item's possible_duplicate/probably_already_handled
// flags at staging time (Phase 12), or the macOS notifications themselves
// (internal/notify) — this package renders whatever badge state the queue
// already holds and never invents suppression: a badge is informational
// only, and the UI must never auto-reject or auto-suppress an item on the
// strength of one (see CLAUDE.md's "invariant that matters most").
//
// # Queue API additions
//
// Building the list view, the sender/subject columns and the
// already-in-portal endpoint required four small additions to
// internal/queue's public API that did not exist before this phase:
// ListByStatus, GetMessage, MarkAlreadyInPortalWithTeaching and
// GetVendorTeachingByIdentityKey. Each is documented at its declaration in
// internal/queue; none duplicates queue's own dedup/lifecycle logic, and
// none required a new file in that package — the vendor_teaching write in
// particular was already anticipated by MarkAlreadyInPortal's doc comment
// ("the webui handler, Phase 8").
package webui
