// DELETE AFTER P1 — see main.go and plan Phase 3/15 (F-07).
package main

import (
	"fmt"
	"strings"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// errNoAdministrations is returned when administrations() came back empty —
// there is nothing to select and nothing to guess (F-01).
var errNoAdministrations = fmt.Errorf("administrations() returned zero administrations")

// errAmbiguousAdministration is returned by selectAdministration when more
// than one administration was returned and override does not disambiguate
// it. The spike must stop and require explicit confirmation rather than
// guess (F-01, AC-1) — never silently pick the first one.
type errAmbiguousAdministration struct {
	candidates []string
}

func (e *errAmbiguousAdministration) Error() string {
	return fmt.Sprintf("more than one administration returned (%s) — re-run with -company-number=<one of these> to confirm which one to use; refusing to guess (F-01)",
		strings.Join(e.candidates, ", "))
}

// selectAdministration implements the F-01/AC-1 guard: with exactly one
// administration, it is selected automatically; with zero, it is an error;
// with more than one, override must name exactly one of the returned
// companyNumbers, or selectAdministration refuses with
// errAmbiguousAdministration rather than guessing.
func selectAdministration(admins []clearfacts.Administration, override string) (string, error) {
	if len(admins) == 0 {
		return "", errNoAdministrations
	}
	if len(admins) == 1 {
		return admins[0].CompanyNumber, nil
	}

	candidates := make([]string, len(admins))
	for i, a := range admins {
		candidates[i] = a.CompanyNumber
	}

	if override == "" {
		return "", &errAmbiguousAdministration{candidates: candidates}
	}
	for _, c := range candidates {
		if c == override {
			return override, nil
		}
	}
	return "", fmt.Errorf("-company-number=%q does not match any returned administration (%s)", override, strings.Join(candidates, ", "))
}
