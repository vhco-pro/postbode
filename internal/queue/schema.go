package queue

// migration is one versioned, ordered batch of DDL statements. Migrations
// are applied at most once, tracked in schema_migrations, so the schema can
// evolve across later phases (L3/L4 in Phase 12, F-57 in the now-dropped
// Phase 14) without a destructive wipe.
type migration struct {
	version int
	stmts   []string
}

// migrations is the ordered, append-only list of schema changes. Never edit
// an already-shipped entry — add a new one instead, so a database created by
// an earlier binary still migrates cleanly forward.
var migrations = []migration{
	{
		version: 1,
		stmts: []string{
			// message — spec §5.2. "from" is quoted because FROM is a SQL
			// keyword; the column name itself is verbatim from the spec.
			`CREATE TABLE message (
				gmail_message_id     TEXT PRIMARY KEY,
				thread_id            TEXT,
				"from"               TEXT,
				subject              TEXT,
				internal_date        TEXT,
				first_seen_at        TEXT NOT NULL,
				all_docs_uploaded_at TEXT,
				labeled_at           TEXT
			)`,
			// Explicit named unique index alongside the PRIMARY KEY, so the
			// F-40/constraints requirement ("unique index on
			// message.gmail_message_id") is directly introspectable rather
			// than only implied by the primary key.
			`CREATE UNIQUE INDEX idx_message_gmail_message_id ON message(gmail_message_id)`,

			// item — spec §5.2. The "flags" field is expanded to one boolean
			// column per named flag (needs_manual_handling, low_confidence,
			// possible_duplicate, probably_already_handled,
			// unsupported_type) — SQLite has no array/set column type, and
			// this is the direct, queryable translation of the spec's flag
			// list, not an invented field.
			`CREATE TABLE item (
				id                        INTEGER PRIMARY KEY AUTOINCREMENT,
				gmail_message_id          TEXT NOT NULL REFERENCES message(gmail_message_id),
				spool_path                TEXT,
				orig_filename             TEXT,
				proposed_filename         TEXT,
				mime_type                 TEXT,
				size_bytes                INTEGER,
				sha256                    TEXT NOT NULL,
				identity_key              TEXT,
				identity_confidence       TEXT,
				identity_source           TEXT,
				status                    TEXT NOT NULL,
				needs_manual_handling     INTEGER NOT NULL DEFAULT 0,
				low_confidence            INTEGER NOT NULL DEFAULT 0,
				possible_duplicate        INTEGER NOT NULL DEFAULT 0,
				probably_already_handled  INTEGER NOT NULL DEFAULT 0,
				unsupported_type          INTEGER NOT NULL DEFAULT 0,
				linked_item_id            INTEGER REFERENCES item(id),
				dedup_layer               TEXT,
				staged_at                 TEXT NOT NULL,
				approved_at               TEXT,
				uploaded_at               TEXT,
				uuid                      TEXT,
				amount_of_pages           INTEGER,
				verified_at               TEXT,
				failed_at                 TEXT,
				last_error                TEXT,
				retry_count               INTEGER NOT NULL DEFAULT 0,
				next_retry_at             TEXT,
				claimed_at                TEXT
			)`,
			`CREATE INDEX idx_item_gmail_message_id ON item(gmail_message_id)`,
			`CREATE INDEX idx_item_status ON item(status)`,
			// unique item.uuid when non-null.
			`CREATE UNIQUE INDEX idx_item_uuid ON item(uuid) WHERE uuid IS NOT NULL`,
			// The Design correction (doc.go, OQ-P1): partial unique index on
			// item.sha256, scoped to the "active" status set. duplicate_linked
			// is deliberately absent from this IN-list, which is what lets a
			// byte-identical second item be stored and linked without
			// colliding with the first item's hash.
			`CREATE UNIQUE INDEX idx_item_sha256_active ON item(sha256)
				WHERE status IN ('staged','approved','uploaded','already_in_portal')`,
			// F-53 foundation: ClaimApproved only ever looks at approved,
			// unclaimed rows.
			`CREATE INDEX idx_item_claimable ON item(status, claimed_at)`,

			// vendor_teaching — spec §5.2. Populated from Phase 12 (F-34/F-35).
			`CREATE TABLE vendor_teaching (
				vendor_domain TEXT PRIMARY KEY,
				identity_key  TEXT,
				reason        TEXT NOT NULL,
				marked_at     TEXT NOT NULL,
				note          TEXT
			)`,

			// sync_state — spec §5.2. Singleton row, populated from Phase 7.
			`CREATE TABLE sync_state (
				id                  INTEGER PRIMARY KEY CHECK (id = 1),
				history_id          TEXT,
				last_poll_at        TEXT,
				label_id_submitted  TEXT,
				token_issued_at     TEXT
			)`,

			// decision_log — spec §5.2. Populated from Phase 6.
			`CREATE TABLE decision_log (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				gmail_message_id    TEXT NOT NULL,
				decision            TEXT NOT NULL,
				matched_rule_index  INTEGER,
				reason              TEXT,
				at                  TEXT NOT NULL
			)`,

			// item_transition — Phase 4 addition, not one of spec §5.2's five
			// named entities. F-41 requires every lifecycle transition to be
			// logged with a timestamp and an actor; §5.2 assigns no entity to
			// hold that log, so this table supplies it without touching any
			// of the five entities' own column lists. from_status is NULL for
			// the initial insert (nothing -> staged / duplicate_linked /
			// suppressed_peppol).
			`CREATE TABLE item_transition (
				id          INTEGER PRIMARY KEY AUTOINCREMENT,
				item_id     INTEGER NOT NULL REFERENCES item(id),
				from_status TEXT,
				to_status   TEXT NOT NULL,
				actor       TEXT NOT NULL,
				at          TEXT NOT NULL
			)`,
			`CREATE INDEX idx_item_transition_item_id ON item_transition(item_id)`,
		},
	},
	{
		// Phase 7 addition: sync_state.last_auth_error exposes F-16/F-17's
		// "re-auth needed" flag through sync_state, alongside history_id,
		// last_poll_at, label_id_submitted and token_issued_at. Additive
		// only — no existing column changes, so a database created by an
		// earlier binary migrates forward with no wipe.
		version: 2,
		stmts: []string{
			`ALTER TABLE sync_state ADD COLUMN last_auth_error TEXT`,
		},
	},
}
