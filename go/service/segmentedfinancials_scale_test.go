package service

import "testing"

// A 10-K prints its segment table in millions. The extractor reports the
// printed figure and the table's stated unit; the record must carry whole
// dollars, or a client comparing against a normalized statement is off by
// a factor of a million.
func TestNormalizeSegmentedFinancials_ScalesByStatedUnit(t *testing.T) {
	data := map[string]any{
		"product_net_sales": []any{
			map[string]any{
				"name": "iPhone", "metric": "net_sales", "unit": "millions",
				"values": []any{map[string]any{"fiscal_year": 2025.0, "period_end": "2025-09-27", "value": 209586.0}},
			},
			map[string]any{
				"name": "Mac", "metric": "net_sales", "unit": nil,
				"values": []any{map[string]any{"fiscal_year": 2025.0, "period_end": "2025-09-27", "value": 5.0}},
			},
		},
	}
	records, err := normalizeSegmentedFinancials(data, "AAPL", "https://sec.gov/x", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, records[0].Object)
	products := body["income_statement"].(map[string]any)["revenue"].(map[string]any)["product"].([]any)
	got := map[string]float64{}
	for _, p := range products {
		row := p.(map[string]any)
		got[row["label"].(string)] = row["value"].(float64)
	}
	if got["iPhone"] != 209586000000 {
		t.Fatalf("iPhone = %v, want 209586000000 (the table is in millions)", got["iPhone"])
	}
	if got["Mac"] != 5 {
		t.Fatalf("Mac = %v, want 5 unchanged (no stated unit means whole units)", got["Mac"])
	}
}
