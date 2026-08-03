// DELETE AFTER P1 — see main.go and plan Phase 3/15 (F-07).
package main

import (
	"context"
	"fmt"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// statisticsTypeCandidates is the undocumented `type` argument's candidate
// set (F-08, OQ-8). The schema does not enumerate permitted values, so this
// is a reasonable guess set covering the plausible English-language and
// literal-enum-value spellings, tried in order until one works.
var statisticsTypeCandidates = []string{
	"DOCUMENTS",
	"INVOICES",
	"PURCHASE",
	"documents",
	"invoices",
	"UPLOADS",
	"INBOX",
	"COUNT",
}

// statisticsCaller abstracts the single companyStatistics call the probe
// makes per candidate, so probeCompanyStatisticsTypes is unit-testable
// without a live or even a fake HTTP round trip.
type statisticsCaller func(ctx context.Context, typeArg string) ([]clearfacts.CompanyStatistic, error)

// statisticsProbeAttempt records the outcome of trying one candidate `type`
// string.
type statisticsProbeAttempt struct {
	Type  string
	Err   error // nil on success
	Stats []clearfacts.CompanyStatistic
}

// statisticsProbeResult is the outcome of the whole F-08 probe: either
// WorkingType is non-empty (the first candidate that succeeded, with its
// Stats), or it is empty and every attempt is recorded with its error —
// "no working type found" is an explicit, printable pass condition per
// AC-5b, not a failure.
type statisticsProbeResult struct {
	WorkingType string
	Stats       []clearfacts.CompanyStatistic
	Attempts    []statisticsProbeAttempt
}

// probeCompanyStatisticsTypes tries each candidate in statisticsTypeCandidates,
// in order, via call, stopping at the first one that succeeds (F-08, AC-5b).
// Every attempt — success or failure — is recorded in Attempts so the caller
// can print exactly which candidates were tried and how each one failed.
func probeCompanyStatisticsTypes(ctx context.Context, call statisticsCaller, candidates []string) statisticsProbeResult {
	var result statisticsProbeResult
	for _, typ := range candidates {
		stats, err := call(ctx, typ)
		result.Attempts = append(result.Attempts, statisticsProbeAttempt{Type: typ, Err: err, Stats: stats})
		if err == nil {
			result.WorkingType = typ
			result.Stats = stats
			return result
		}
	}
	return result
}

// formatStatisticsProbeResult renders the F-08/AC-5b probe outcome as the
// printable lines the spike emits: either the working type with its
// {period, value} items, or an explicit "no working type found" line, plus
// a per-candidate line so a run can be audited afterward.
func formatStatisticsProbeResult(res statisticsProbeResult) string {
	out := ""
	for _, a := range res.Attempts {
		if a.Err == nil {
			out += fmt.Sprintf("  companyStatistics(type=%q): OK\n", a.Type)
		} else {
			out += fmt.Sprintf("  companyStatistics(type=%q): error: %v\n", a.Type, a.Err)
		}
	}
	if res.WorkingType == "" {
		out += "no working `type` found for companyStatistics — F-57 aggregate reconciliation (Phase 14) is dropped (OQ-8)\n"
		return out
	}
	out += fmt.Sprintf("working type=%q:\n", res.WorkingType)
	for _, stat := range res.Stats {
		for _, item := range stat.Items {
			out += fmt.Sprintf("  companyNumber=%s period=%v value=%v\n", stat.CompanyNumber, item.Period, item.Value)
		}
	}
	return out
}
