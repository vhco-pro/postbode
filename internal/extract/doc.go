// Package extract turns one raw Gmail message into zero or more Postbode
// review-queue items (spec §3.3, F-20…F-25; plan Phase 5).
//
// # What it does
//
//  1. Walks the message's full MIME tree — including nesting inside
//     multipart/mixed and multipart/related — collecting application/pdf
//     parts, plus application/octet-stream parts whose filename ends
//     .pdf (F-20).
//  2. Spools every collected candidate's decoded bytes to disk at mode
//     0600 (F-24).
//  3. Sniffs each spooled file's real content against the ClearFacts
//     accepted MIME list (application/pdf, image/jpeg, application/xml),
//     staging anything else as unsupported_type with the sniffed type
//     recorded (F-25).
//  4. Detects password-protected/undecryptable PDFs via a stdlib byte
//     scan for the /Encrypt trailer key, staging them
//     needs_manual_handling — never dropped, never auto-uploaded (F-22).
//  5. Builds a deterministic proposed filename
//     {vendor}-{date}-{orig}.pdf (F-23).
//  6. Stages exactly one queue item per candidate document via
//     queue.DB.StageItem (F-21) — which itself performs the F-31 (L2)
//     SHA-256 dedup and F-44 rejection-memory checks. This package never
//     reimplements either; it only calls queue's public API.
//
// # Fail-safe ordering (spec §8 — the crux of this phase)
//
// Every candidate document is spooled to disk BEFORE the message is
// recorded as seen (L1, queue.DB.RecordMessageIfNew) and BEFORE any queue
// row is staged. If any spool write fails — disk full being the
// motivating case — ExtractMessage aborts the whole message: any partial
// spool writes already made are best-effort removed, the message is
// NEVER marked seen, and no queue row is ever committed. The caller's
// next poll cycle will see the same message again and retry it from
// scratch. The alternative ordering — recording the message as seen
// first — would permanently lose an invoice the moment the disk fills up
// even slightly, which is exactly the G-1 failure this package exists to
// prevent.
//
// # Never crashes
//
// No amount of malformed MIME, truncated parts, bad transfer encodings,
// or missing headers causes a panic (NF-06). Anything that cannot be
// parsed or decoded is recorded as a warning on Result and the affected
// part is simply not extracted — it is never silently dropped from the
// caller's view (G-1): every part that isn't collected produces either a
// logged warning or a staged item (unsupported_type / needs_manual_handling).
package extract
