package clearfacts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// companyStatisticsQuery is the only portal-side aggregate signal that
// exists (spec §6.1). The permitted `type` argument strings are
// undocumented — that is exactly what the Phase 3 probe (F-08) discovers by
// trying candidates, so Type is always caller-supplied here, never
// hardcoded.
const companyStatisticsQuery = `query CompanyStatistics($type: String!, $startPeriod: Date!, $endPeriod: Date!, $companyNumber: String, $invoicetype: InvoiceType) {
  companyStatistics(type: $type, startPeriod: $startPeriod, endPeriod: $endPeriod, companyNumber: $companyNumber, invoicetype: $invoicetype) {
    companyNumber
    items { period value }
  }
}`

// CompanyStatisticsInput carries the caller-supplied candidate Type string
// the Phase 3 probe iterates over, plus the period bounds and optional
// filters.
type CompanyStatisticsInput struct {
	// Type is the candidate `type` argument string being probed. Required —
	// there is no default, on purpose (F-08).
	Type          string
	StartPeriod   string
	EndPeriod     string
	CompanyNumber string
	InvoiceType   string
}

// StatisticItem is one {period, value} pair from a companyStatistics result.
type StatisticItem struct {
	Period []string `json:"period"`
	Value  []int    `json:"value"`
}

// CompanyStatistic is one companyStatistics result entry.
type CompanyStatistic struct {
	CompanyNumber string          `json:"companyNumber"`
	Items         []StatisticItem `json:"items"`
}

// CompanyStatistics calls companyStatistics with a caller-supplied `type`
// argument (F-08). A non-working type string surfaces as a classified
// GraphQL error via Classify — it is never silently swallowed, since the
// whole point of the Phase 3 probe is to observe which candidates fail.
func (c *Client) CompanyStatistics(ctx context.Context, in CompanyStatisticsInput) ([]CompanyStatistic, error) {
	if in.Type == "" {
		return nil, errors.New("clearfacts: CompanyStatistics: Type is required (the Phase 3 probe supplies the candidate)")
	}
	if in.StartPeriod == "" || in.EndPeriod == "" {
		return nil, errors.New("clearfacts: CompanyStatistics: StartPeriod and EndPeriod are required")
	}

	variables := map[string]any{
		"type":        in.Type,
		"startPeriod": in.StartPeriod,
		"endPeriod":   in.EndPeriod,
	}
	if in.CompanyNumber != "" {
		variables["companyNumber"] = in.CompanyNumber
	}
	if in.InvoiceType != "" {
		variables["invoicetype"] = in.InvoiceType
	}

	raw, err := c.doJSON(ctx, companyStatisticsQuery, variables)
	if err != nil {
		return nil, err
	}

	var payload struct {
		CompanyStatistics []CompanyStatistic `json:"companyStatistics"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("clearfacts: decode companyStatistics response: %w", err)
	}
	return payload.CompanyStatistics, nil
}
