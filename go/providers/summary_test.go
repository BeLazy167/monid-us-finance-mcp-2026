package providers

import (
	"encoding/json"
	"math"
	"testing"
)

// summaryFixture mirrors tests/test_service.py's SUMMARY constant.
const summaryFixture = `{
	"currentPrice": 230.1,
	"marketCap": 3400000000000,
	"trailingPE": 34.2,
	"priceToBook": 55.0,
	"priceToRevenue": 8.9,
	"enterpriseValueToEbitda": 27.5,
	"priceChange1d": 1.2,
	"priceChangePercentage1d": 0.5,
	"revenueTTM": 400.0,
	"grossProfitTTM": 180.0,
	"earningsTTM": 100.0,
	"operatingProfitMarginTTM": 0.3,
	"updatedAt": "2026-09-03T20:00:00Z"
}`

// TestBuildPriceSnapshot ports
// tests/test_service.py::test_stock_price_snapshot.
func TestBuildPriceSnapshot(t *testing.T) {
	summary, err := ParseSummary(json.RawMessage(summaryFixture))
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	snapshot, err := BuildPriceSnapshot("AAPL", summary)
	if err != nil {
		t.Fatalf("BuildPriceSnapshot: %v", err)
	}
	if *snapshot.Price != 230.1 {
		t.Fatalf("got price=%v, want 230.1", *snapshot.Price)
	}
	assertStrPtr(t, "ticker", snapshot.Ticker, "AAPL")
	if *snapshot.DayChange != 1.2 {
		t.Fatalf("got day_change=%v, want 1.2", *snapshot.DayChange)
	}
	if *snapshot.DayChangePercent != 0.5 {
		t.Fatalf("got day_change_percent=%v, want 0.5", *snapshot.DayChangePercent)
	}
	assertStrPtr(t, "time", snapshot.Time, "2026-09-03T20:00:00Z")
}

func TestBuildPriceSnapshot_RequiresFinitePrice(t *testing.T) {
	summary, err := ParseSummary(json.RawMessage(`{"marketCap": 100}`))
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	if _, err := BuildPriceSnapshot("AAPL", summary); err == nil {
		t.Fatal("expected a schema-drift error: no finite current price in the summary")
	}
}

func TestParseSummary_RequiresAtLeastOneRecognizedNumericField(t *testing.T) {
	_, err := ParseSummary(json.RawMessage(`{"currency": "USD"}`))
	if err == nil {
		t.Fatal("expected a schema-drift error: no recognized numeric field present")
	}
}

// TestBuildFinancialMetricSnapshot ports
// tests/test_service.py::test_financial_metrics_snapshot_shape.
func TestBuildFinancialMetricSnapshot(t *testing.T) {
	summary, err := ParseSummary(json.RawMessage(summaryFixture))
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	snapshot := BuildFinancialMetricSnapshot("AAPL", summary)
	assertStrPtr(t, "ticker", snapshot.Ticker, "AAPL")
	if *snapshot.MarketCap != 3_400_000_000_000 {
		t.Fatalf("got market_cap=%v, want 3400000000000", *snapshot.MarketCap)
	}
	if *snapshot.PriceToEarningsRatio != 34.2 {
		t.Fatalf("got price_to_earnings_ratio=%v, want 34.2", *snapshot.PriceToEarningsRatio)
	}
	if got := *snapshot.GrossMargin; math.Abs(got-0.45) > 1e-9 { // 180/400
		t.Fatalf("got gross_margin=%v, want 0.45", got)
	}
	if got := *snapshot.NetMargin; math.Abs(got-0.25) > 1e-9 { // 100/400
		t.Fatalf("got net_margin=%v, want 0.25", got)
	}
	if got := *snapshot.OperatingMargin; math.Abs(got-0.3) > 1e-9 { // already a ratio, unchanged
		t.Fatalf("got operating_margin=%v, want 0.3", got)
	}
}

// TestAsRatio_ConvertsPercentagesButNotRatios pins the verified DefiLlama
// rule (service._as_ratio): |value| > 1.5 is treated as a percentage and
// divided by 100; anything else is passed through unchanged.
func TestAsRatio_ConvertsPercentagesButNotRatios(t *testing.T) {
	cases := []struct {
		name  string
		value float64
		want  float64
	}{
		{"already a ratio", 0.3, 0.3},
		{"boundary ratio just under threshold", 1.5, 1.5},
		{"a percentage above threshold", 30, 0.3},
		{"a large negative percentage", -45, -0.45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := asRatio(&tc.value)
			if math.Abs(*got-tc.want) > 1e-9 {
				t.Fatalf("asRatio(%v) = %v, want %v", tc.value, *got, tc.want)
			}
		})
	}
	if asRatio(nil) != nil {
		t.Fatal("asRatio(nil) should stay nil")
	}
}

func TestRatio_NilOnMissingOrZeroDenominator(t *testing.T) {
	one := 10.0
	zero := 0.0
	if ratio(nil, &one) != nil {
		t.Fatal("ratio(nil, x) should be nil")
	}
	if ratio(&one, nil) != nil {
		t.Fatal("ratio(x, nil) should be nil")
	}
	if ratio(&one, &zero) != nil {
		t.Fatal("ratio(x, 0) should be nil")
	}
}
