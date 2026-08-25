package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// Verifies: Plan: Resilient poll — per-message failure budget and stall escalation, Criterion: "Do not edit versions 1–3 (NF-07, NF-15). Add a test that asserts the shipped versions 1–3 statement lists are byte-identical to what they are today, so a future edit fails loudly."
//
// The migrations slice is append-only by contract: a database created by an
// earlier binary must still migrate cleanly forward. That contract lives
// only in a comment, and a comment cannot fail CI. This test can.
//
// If you are here because this test failed: you edited an already-shipped
// migration. Do not update the hash to match — add a NEW migration entry
// instead. The only legitimate reason to touch these constants is if a
// shipped migration was never actually released to any database.
func TestShippedMigrationsAreFrozen(t *testing.T) {
	for _, m := range migrations {
		if m.version > 3 {
			continue
		}
		got := hashStatements(m.stmts)
		want, ok := shippedMigrationHashes[m.version]
		if !ok {
			t.Fatalf("migration version %d has no frozen hash recorded", m.version)
		}
		if got != want {
			t.Errorf(`migration version %d has been EDITED.

An already-shipped migration must never change: a database created by an
earlier binary would silently skip the edit, because schema_migrations
already records that version as applied. The two databases then differ
while both claim to be at the same version.

Add a NEW migration entry instead of editing this one. Only update the
frozen hash if this migration was never released.

  got  %s
  want %s`, m.version, got, want)
		}
	}

	// A version must never be reused or go backwards either.
	seen := map[int]bool{}
	prev := 0
	for _, m := range migrations {
		if seen[m.version] {
			t.Errorf("migration version %d appears more than once", m.version)
		}
		if m.version <= prev {
			t.Errorf("migration version %d is out of order after %d; the slice must ascend", m.version, prev)
		}
		seen[m.version] = true
		prev = m.version
	}
}

// shippedMigrationHashes pins the DDL of every migration that has been
// released to a real database. See TestShippedMigrationsAreFrozen.
var shippedMigrationHashes = map[int]string{
	1: "d0f2b60122c285af774f2ff8efc056e5a5b17bd7ef0cdf3efedbc50b755e223b",
	2: "28b2d96f69b38ea70623ce5daea76eeb45714a91d2451a32f864eb6f1a6af971",
	3: "83185470fbde372c4654f58c064a15f9be9902c737396c58cf9271b87051dbfc",
}

func hashStatements(stmts []string) string {
	h := sha256.New()
	for _, s := range stmts {
		// Normalise indentation so gofmt-driven whitespace churn does not
		// read as a semantic edit; the SQL itself is what is frozen.
		h.Write([]byte(strings.Join(strings.Fields(s), " ")))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
