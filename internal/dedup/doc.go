// Package dedup implements the pure, content-independent half of
// Postbode's Layer 3 (invoice identity key, F-32/F-33) and Layer 4
// (vendor teaching / known-Peppol, F-34/F-35/F-36) duplicate-prevention
// layers (spec §3.5, plan Phase 12).
//
// Everything in this package is a pure function of its inputs: no SQLite
// access, no filesystem access, no network. The stateful half — looking up
// whether a parsed identity key or vendor domain matches an earlier item
// or a taught vendor, and persisting the resulting flags — lives in
// internal/queue (StageItem), which is the only place close enough to the
// insert to do the match-and-flag atomically. This package exists so that
// the heuristic parsing at the heart of L3 can be tested exhaustively in
// isolation, with zero database setup, and so a future PDF-text-layer
// source (deferred, see plan OQ-P4) can be added as one more input to
// ParseIdentity without reshaping the identity key itself.
//
// # The invariant that matters most
//
// Nothing in this package ever decides to drop, skip or auto-reject a
// document. A parse failure degrades to an empty Identity (no key), which
// queue.StageItem simply leaves unset — the item still stages. A glob
// match against vendors.known_peppol still results in a staged item (in
// status suppressed_peppol), never a discarded one. See CLAUDE.md and
// ADR-001: "L3 and L4 must NEVER auto-suppress." A wrong high-confidence
// key is worse than no key at all — every parser in this file is written
// to prefer returning nothing over guessing wrong.
package dedup
