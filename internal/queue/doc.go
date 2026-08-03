// Package queue implements the Postbode review queue: the SQLite-backed
// durable store for message/item state, the F-41 item lifecycle, and the two
// silent local duplicate-prevention layers, L1 (Gmail message-id ledger) and
// L2 (SHA-256 content hash).
//
// Everything downstream of extraction writes here (spec §5.2). The schema is
// on modernc.org/sqlite — pure Go, no cgo (NF-01) — opened in WAL mode. A
// small migration runner (schema_migrations) lets the schema evolve in later
// phases (L3/L4 in Phase 12) without a destructive wipe.
//
// # The duplicate_linked status
//
// Spec §5.2 declares a partial unique index on item.sha256 scoped to the
// "active" statuses (staged, approved, uploaded, already_in_portal), while
// F-31/AC-11 require a byte-identical second item to be stored and linked,
// not silently discarded. Those two statements are incompatible if the
// linked duplicate were stored as staged. This package resolves it exactly
// as the spec's v1.3 ratification note (OQ-P1) directs: a byte-identical
// arrival is inserted with status=duplicate_linked, which sits deliberately
// OUTSIDE the partial index's predicate. That is what lets the second item
// exist and be visible (F-31/AC-11) while the index continues to guarantee
// at most one *live* item per hash.
//
// # Concurrency model
//
// SQLite allows exactly one writer. The *DB returned by Open restricts the
// underlying connection pool to a single connection (SetMaxOpenConns(1)), so
// every transaction — StageItem, a lifecycle transition, ClaimApproved — is
// naturally serialized by database/sql itself rather than racing on
// SQLITE_BUSY. ClaimApproved's select-and-mark happens inside one
// transaction so a concurrent or restarted uploader (Phase 9) sees zero
// claimable rows once an item has been claimed.
package queue
