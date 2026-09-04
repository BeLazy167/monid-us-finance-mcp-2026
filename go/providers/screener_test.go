package providers

import (
	"encoding/json"
	"testing"
)

// screenerFixture mirrors tests/test_gate_b.py's SCREENER constant.
const screenerFixture = `{
	"status": "success",
	"data": {
		"data": {
			"filters": {},
			"table": {
				"asOf": null,
				"headers": {
					"symbol": "Symbol",
					"name": "Name",
					"lastsale": "Last Sale",
					"netchange": "Net Change",
					"pctchange": "% Change",
					"marketCap": "Market Cap"
				},
				"rows": [
					{
						"symbol": "AAPL",
						"name": "Apple Inc. Common Stock",
						"lastsale": "$328.21",
						"netchange": "-3.25",
						"pctchange": "-1.00%",
						"marketCap": "4,789,955,817,800",
						"url": "/market-activity/stocks/aapl"
					}
				]
			},
			"totalrecords": 33,
			"asof": "Last price as of Sep 3, 2026"
		},
		"message": null,
		"status": {"rCode": 200, "bCodeMessage": null, "developerMessage": null}
	}
}`

// TestValidateScreenerRequest ports
// tests/test_gate_b.py::test_screen_stocks_rejects_unsupported_filters (the
// validation half) plus the request-shape assertions implicit in
// test_screen_stocks_matches_fd_contract.
func TestValidateScreenerRequest(t *testing.T) {
	t.Run("accepts a supported exchange filter", func(t *testing.T) {
		req, err := ValidateScreenerRequest(
			[]map[string]any{{"field": "exchange", "operator": "eq", "value": "NASDAQ"}}, 10, 0,
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.QueryParams["exchange"] != "NASDAQ" {
			t.Fatalf("got %v, want exchange=NASDAQ", req.QueryParams)
		}
	})
	t.Run("rejects an unsupported filter field", func(t *testing.T) {
		_, err := ValidateScreenerRequest(
			[]map[string]any{{"field": "revenue", "operator": "gt", "value": float64(1_000_000_000)}}, 10, 0,
		)
		if err == nil {
			t.Fatal("expected an error for an unsupported filter field")
		}
	})
	t.Run("rejects no filters", func(t *testing.T) {
		_, err := ValidateScreenerRequest(nil, 10, 0)
		if err == nil {
			t.Fatal("expected an error when filters is empty")
		}
	})
	t.Run("rejects more than two filters", func(t *testing.T) {
		_, err := ValidateScreenerRequest([]map[string]any{
			{"field": "exchange", "operator": "eq", "value": "NASDAQ"},
			{"field": "market_cap", "operator": "eq", "value": "mega"},
			{"field": "exchange", "operator": "eq", "value": "NYSE"},
		}, 10, 0)
		if err == nil {
			t.Fatal("expected an error for more than two filters")
		}
	})
	t.Run("rejects a duplicate field", func(t *testing.T) {
		_, err := ValidateScreenerRequest([]map[string]any{
			{"field": "exchange", "operator": "eq", "value": "NASDAQ"},
			{"field": "exchange", "operator": "eq", "value": "NYSE"},
		}, 10, 0)
		if err == nil {
			t.Fatal("expected an error for a field appearing twice")
		}
	})
	t.Run("rejects a non-eq operator", func(t *testing.T) {
		_, err := ValidateScreenerRequest(
			[]map[string]any{{"field": "exchange", "operator": "gt", "value": "NASDAQ"}}, 10, 0,
		)
		if err == nil {
			t.Fatal("expected an error for a non-eq operator")
		}
	})
	t.Run("rejects an invalid market_cap value", func(t *testing.T) {
		_, err := ValidateScreenerRequest(
			[]map[string]any{{"field": "market_cap", "operator": "eq", "value": "huge"}}, 10, 0,
		)
		if err == nil {
			t.Fatal("expected an error for an unsupported market_cap value")
		}
	})
	t.Run("rejects an extra filter key", func(t *testing.T) {
		_, err := ValidateScreenerRequest(
			[]map[string]any{{"field": "exchange", "operator": "eq", "value": "NASDAQ", "extra": 1}}, 10, 0,
		)
		if err == nil {
			t.Fatal("expected an error for an extra filter key")
		}
	})
	t.Run("rejects limit out of range", func(t *testing.T) {
		_, err := ValidateScreenerRequest(
			[]map[string]any{{"field": "exchange", "operator": "eq", "value": "NASDAQ"}}, 0, 0,
		)
		if err == nil {
			t.Fatal("expected an error for limit=0")
		}
	})
}

// TestNormalizeScreenerAndBuildSearchResults ports
// tests/test_gate_b.py::test_screen_stocks_matches_fd_contract.
func TestNormalizeScreenerAndBuildSearchResults(t *testing.T) {
	rows, err := NormalizeScreener(json.RawMessage(screenerFixture))
	if err != nil {
		t.Fatalf("NormalizeScreener: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	exchange := "NASDAQ"
	results := BuildSearchResults(rows, &exchange, 10)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	result := results[0]
	if result.Ticker != "AAPL" {
		t.Fatalf("got ticker=%q, want AAPL", result.Ticker)
	}
	assertStrPtr(t, "exchange", result.Exchange, "NASDAQ")
	assertStrPtr(t, "market_cap", result.MarketCap, "4789955817800")
	assertStrPtr(t, "last_sale", result.LastSale, "328.21")
	assertStrPtr(t, "net_change", result.NetChange, "-3.25")
	assertStrPtr(t, "percent_change", result.PercentChange, "-0.01")
}

func TestNormalizeScreener_RejectsChangedHeaders(t *testing.T) {
	broken := `{"status":"success","data":{"data":{"filters":{},"table":{"headers":{"symbol":"S"},"rows":[]},"totalrecords":0,"asof":"Last price as of Sep 3, 2026"},"status":{"rCode":200}}}`
	_, err := NormalizeScreener(json.RawMessage(broken))
	if err == nil {
		t.Fatal("expected a schema-drift error for changed headers")
	}
}

func TestNormalizeScreener_RejectsNonSuccessStatus(t *testing.T) {
	broken := `{"status":"error","data":{}}`
	_, err := NormalizeScreener(json.RawMessage(broken))
	if err == nil {
		t.Fatal("expected a schema-drift error for a non-success status")
	}
}

func TestScreenerFilters_IsStatic(t *testing.T) {
	catalog := ScreenerFilters()
	if len(catalog.Operators) != 1 || catalog.Operators[0] != "eq" {
		t.Fatalf("got operators=%v, want [eq]", catalog.Operators)
	}
	company := catalog.Metrics["company"]
	if len(company) != 2 {
		t.Fatalf("got %d company filter fields, want 2", len(company))
	}
	fields := map[string]bool{}
	for _, f := range company {
		fields[f.Field] = true
	}
	if !fields["exchange"] || !fields["market_cap"] {
		t.Fatalf("got fields=%v, want exchange and market_cap", fields)
	}
}
