package clearfacts

import (
	"context"
	"encoding/json"
	"fmt"
)

// administrationsQuery lists the accessible administrations, each carrying
// companyNumber — the value later passed as uploadFile's companyNumber
// argument. Never vatnumber (A-12, F-01).
const administrationsQuery = `query Administrations($offset: Int, $first: Int) {
  administrations(offset: $offset, first: $first) {
    companyNumber
  }
}`

// Administration is one entry of the administrations query result.
type Administration struct {
	CompanyNumber string `json:"companyNumber"`
}

// Administrations lists the PAT's accessible administrations (F-01). offset
// and first are omitted from the request when zero, letting the server apply
// its own defaults.
func (c *Client) Administrations(ctx context.Context, offset, first int) ([]Administration, error) {
	variables := map[string]any{}
	if offset != 0 {
		variables["offset"] = offset
	}
	if first != 0 {
		variables["first"] = first
	}

	raw, err := c.doJSON(ctx, administrationsQuery, variables)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Administrations []Administration `json:"administrations"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("clearfacts: decode administrations response: %w", err)
	}
	return payload.Administrations, nil
}
