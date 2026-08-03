package main

import (
	"errors"
	"testing"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "require **explicit confirmation** if more than one is returned (never guess), write the chosen value to `~/.config/postbode/config.yaml` under `administration.company_number` (F-01, AC-1)"
func TestSelectAdministrationSingleIsAutomatic(t *testing.T) {
	got, err := selectAdministration([]clearfacts.Administration{{CompanyNumber: "BE0123456789"}}, "")
	if err != nil {
		t.Fatalf("selectAdministration: %v", err)
	}
	if got != "BE0123456789" {
		t.Errorf("selectAdministration() = %q, want %q", got, "BE0123456789")
	}
}

func TestSelectAdministrationZeroIsAnError(t *testing.T) {
	_, err := selectAdministration(nil, "")
	if err == nil {
		t.Error("selectAdministration with zero administrations: want an error, got nil")
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "If `administrations` returns more than one administration, **stop and require explicit confirmation** — never guess which one (F-01, AC-1)"
func TestSelectAdministrationMultipleWithoutOverrideRefusesToGuess(t *testing.T) {
	admins := []clearfacts.Administration{{CompanyNumber: "BE0111111111"}, {CompanyNumber: "BE0222222222"}}

	_, err := selectAdministration(admins, "")
	if err == nil {
		t.Fatal("selectAdministration with multiple administrations and no override: want an error, got nil")
	}
	var ambiguous *errAmbiguousAdministration
	if !errors.As(err, &ambiguous) {
		t.Errorf("selectAdministration() err = %v, want an *errAmbiguousAdministration", err)
	}
}

func TestSelectAdministrationMultipleWithMatchingOverride(t *testing.T) {
	admins := []clearfacts.Administration{{CompanyNumber: "BE0111111111"}, {CompanyNumber: "BE0222222222"}}

	got, err := selectAdministration(admins, "BE0222222222")
	if err != nil {
		t.Fatalf("selectAdministration: %v", err)
	}
	if got != "BE0222222222" {
		t.Errorf("selectAdministration() = %q, want %q", got, "BE0222222222")
	}
}

func TestSelectAdministrationMultipleWithNonMatchingOverride(t *testing.T) {
	admins := []clearfacts.Administration{{CompanyNumber: "BE0111111111"}, {CompanyNumber: "BE0222222222"}}

	_, err := selectAdministration(admins, "BE0999999999")
	if err == nil {
		t.Error("selectAdministration with a non-matching override: want an error, got nil")
	}
}
