package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vhco-pro/postbode/internal/clearfacts"
)

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "iterating candidate `type` strings, printing the working one with its `{period,value}` items or an explicit "no working `type` found" (F-08, AC-5b)"
func TestProbeCompanyStatisticsTypesStopsAtFirstWorkingCandidate(t *testing.T) {
	var tried []string
	call := func(_ context.Context, typ string) ([]clearfacts.CompanyStatistic, error) {
		tried = append(tried, typ)
		if typ == "PURCHASE" {
			return []clearfacts.CompanyStatistic{{
				CompanyNumber: "BE0123456789",
				Items:         []clearfacts.StatisticItem{{Period: []string{"2026-08"}, Value: []int{3}}},
			}}, nil
		}
		return nil, errors.New("invalid type argument")
	}

	res := probeCompanyStatisticsTypes(context.Background(), call, []string{"DOCUMENTS", "INVOICES", "PURCHASE", "documents"})

	if res.WorkingType != "PURCHASE" {
		t.Fatalf("WorkingType = %q, want %q", res.WorkingType, "PURCHASE")
	}
	if len(tried) != 3 {
		t.Errorf("tried %d candidates, want exactly 3 (stop at first success)", len(tried))
	}
	if len(res.Stats) != 1 || len(res.Stats[0].Items) != 1 {
		t.Errorf("Stats = %+v, want one statistic with one item", res.Stats)
	}
	if len(res.Attempts) != 3 {
		t.Errorf("Attempts recorded %d entries, want 3", len(res.Attempts))
	}
}

// Verifies: Plan: Postbode — Gmail to ClearFacts/QPS Invoice Agent, Phase 3, Criterion: "print the working `type` with its `{period, value}` items, or an explicit "no working `type` found" (F-08)"
func TestProbeCompanyStatisticsTypesNoneWorking(t *testing.T) {
	call := func(_ context.Context, typ string) ([]clearfacts.CompanyStatistic, error) {
		return nil, errors.New("invalid type argument: " + typ)
	}

	res := probeCompanyStatisticsTypes(context.Background(), call, statisticsTypeCandidates)

	if res.WorkingType != "" {
		t.Errorf("WorkingType = %q, want empty (no candidate worked)", res.WorkingType)
	}
	if len(res.Attempts) != len(statisticsTypeCandidates) {
		t.Errorf("Attempts recorded %d entries, want %d (every candidate tried)", len(res.Attempts), len(statisticsTypeCandidates))
	}
	out := formatStatisticsProbeResult(res)
	if !strings.Contains(out, "no working `type` found") {
		t.Errorf("formatStatisticsProbeResult() = %q, want it to contain the explicit no-working-type message", out)
	}
}

func TestFormatStatisticsProbeResultReportsEveryAttempt(t *testing.T) {
	res := statisticsProbeResult{
		Attempts: []statisticsProbeAttempt{
			{Type: "DOCUMENTS", Err: errors.New("boom")},
			{Type: "PURCHASE", Err: nil},
		},
		WorkingType: "PURCHASE",
	}
	out := formatStatisticsProbeResult(res)
	if !strings.Contains(out, "DOCUMENTS") || !strings.Contains(out, "PURCHASE") {
		t.Errorf("formatStatisticsProbeResult() = %q, want it to name every candidate tried", out)
	}
}
