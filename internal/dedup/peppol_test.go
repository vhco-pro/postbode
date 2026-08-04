package dedup_test

import (
	"testing"

	"github.com/vhco-pro/postbode/internal/dedup"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 12, Criterion: "`vendors.known_peppol` glob list: matching documents stage `suppressed_peppol` and are not uploadable without an explicit UI override action (F-36, AC-14)"
func TestMatchesKnownPeppol(t *testing.T) {
	tests := []struct {
		name   string
		sender string
		globs  []string
		want   bool
	}{
		{"bare address matches glob", "facturatie@acerta.be", []string{"*@acerta.be"}, true},
		{"display-name address matches glob", "Acerta <facturatie@acerta.be>", []string{"*@acerta.be"}, true},
		{"case-insensitive match", "Facturatie@Acerta.BE", []string{"*@acerta.be"}, true},
		{"no glob configured", "facturatie@acerta.be", nil, false},
		{"non-matching vendor", "billing@ovh.com", []string{"*@acerta.be"}, false},
		{"matches second of several globs", "billing@ovh.com", []string{"*@acerta.be", "*@ovh.com"}, true},
		{"empty sender never matches", "", []string{"*@acerta.be"}, false},
		{"malformed glob pattern is skipped, not fatal", "facturatie@acerta.be", []string{"[", "*@acerta.be"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dedup.MatchesKnownPeppol(tt.sender, tt.globs); got != tt.want {
				t.Errorf("MatchesKnownPeppol(%q, %v) = %v, want %v", tt.sender, tt.globs, got, tt.want)
			}
		})
	}
}
