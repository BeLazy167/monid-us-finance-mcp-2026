package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/belazy/monid-finance/providers"
)

// ohlcvFixture is three consecutive daily bars, newest last.
const ohlcvFixture = `[
	[1767052800, 10, 14, 9, 13, 100],
	[1767139200, 20, 24, 19, 23, 200],
	[1767225600, 30, 34, 29, 33, 300]
]`

// TestPriceWindowBoundsKeepEveryBar pins the bug that left every
// market-relative metric absent in production. The OHLCV normaliser
// compares dates as strings, so "" as the upper bound is not
// "unbounded": it is smaller than every real date and discards the
// whole series.
func TestPriceWindowBoundsKeepEveryBar(t *testing.T) {
	kept, err := providers.NormalizePrices(json.RawMessage(ohlcvFixture), earliestDay, latestDay, "day")
	if err != nil {
		t.Fatalf("NormalizePrices: %v", err)
	}
	if len(kept) != 3 {
		t.Fatalf("got %d bars within the widest window, want 3", len(kept))
	}

	dropped, err := providers.NormalizePrices(json.RawMessage(ohlcvFixture), "", "", "day")
	if err != nil {
		t.Fatalf("NormalizePrices with empty bounds: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("empty bounds kept %d bars; the constants above exist because they keep none", len(dropped))
	}
}

// quarterRow is one quarter of income statement values.
func quarterRow(day string, eps, revenue float64) providers.PeriodRow {
	period, err := time.Parse(dateLayout, day)
	if err != nil {
		panic(err)
	}
	return providers.PeriodRow{ReportPeriod: period, Values: map[string]any{
		"EPS (Diluted)":                eps,
		"Revenue":                      revenue,
		"Shares Outstanding (Diluted)": 1000.0,
	}}
}

// TestPeriodValuesSumsTTM proves a ttm row is valued against four
// quarters of earnings rather than the newest quarter alone. Reading one
// quarter priced Apple at 151 times earnings instead of 35.
func TestPeriodValuesSumsTTM(t *testing.T) {
	series := providers.StatementSeries{Quarterly: []providers.PeriodRow{
		quarterRow("2025-03-31", 1.50, 90),
		quarterRow("2025-06-30", 1.00, 85),
		quarterRow("2025-09-30", 2.00, 95),
		quarterRow("2025-12-31", 2.50, 100),
	}}
	asOf, err := time.Parse(dateLayout, "2025-12-31")
	if err != nil {
		t.Fatalf("parse asOf: %v", err)
	}

	ttm := periodValues(series, "income", "ttm", asOf)
	eps, ok := number(ttm, "EPS (Diluted)")
	if !ok {
		t.Fatal("ttm row states no diluted EPS")
	}
	if eps != 7.00 {
		t.Fatalf("ttm EPS is %v, want 7.00 (the four quarters summed)", eps)
	}
	// Shares are averaged, not summed: a share count is a level, not a flow.
	if shares, _ := number(ttm, "Shares Outstanding (Diluted)"); shares != 1000 {
		t.Fatalf("ttm share count is %v, want 1000", shares)
	}

	quarter := periodValues(series, "income", "quarterly", asOf)
	if eps, _ := number(quarter, "EPS (Diluted)"); eps != 2.50 {
		t.Fatalf("quarterly EPS is %v, want the newest quarter's 2.50", eps)
	}
}

// TestValuationFieldsPriceTheRow checks the arithmetic each metric is
// built from, and that a metric whose input is missing is left out
// rather than reported as a zero.
func TestValuationFieldsPriceTheRow(t *testing.T) {
	in := valuationInputs{
		Shares: 1000, hasShares: true,
		EPS: 7, hasEPS: true,
		Revenue: 370, hasRevenue: true,
		Equity: 2000, hasEquity: true,
		FreeCashFlow: 500, hasFreeCashFlow: true,
		Debt: 300, Cash: 100,
	}
	got := valuationFields(50, in)

	for key, want := range map[string]float64{
		"market_cap":                        50_000,
		"price_to_earnings_ratio":           50.0 / 7.0,
		"book_value_per_share":              2.0,
		"price_to_book_ratio":               25.0,
		"free_cash_flow_per_share":          0.5,
		"price_to_sales_ratio":              50_000.0 / 370.0,
		"free_cash_flow_yield":              500.0 / 50_000.0,
		"enterprise_value":                  50_200,
		"enterprise_value_to_revenue_ratio": 50_200.0 / 370.0,
	} {
		value, ok := got[key].(float64)
		if !ok {
			t.Fatalf("%s is absent", key)
		}
		if value != want {
			t.Fatalf("%s is %v, want %v", key, value, want)
		}
	}
	// EBITDA was never stated, so its multiple must not be invented.
	if _, present := got["enterprise_value_to_ebitda_ratio"]; present {
		t.Fatal("enterprise_value_to_ebitda_ratio was reported without an EBITDA")
	}
	// Growth was never stated, so PEG must not be invented either.
	if _, present := got["peg_ratio"]; present {
		t.Fatal("peg_ratio was reported without an earnings growth rate")
	}
}

// TestDerivedRatiosMatchFinancialDatasets pins the two formulas whose
// shape was read off Financial Datasets' own responses rather than a
// textbook. Both were confirmed against AAPL, MSFT, NVDA and KO on
// 2026-09-05; Apple's figures are the ones used here.
func TestDerivedRatiosMatchFinancialDatasets(t *testing.T) {
	in := valuationInputs{
		Shares: 14_084_000_000, hasShares: true,
		EBIT: 155_906_000_000, hasEBIT: true,
		EBITDA: 169_006_000_000, hasEBITDA: true,
		Equity: 107_520_000_000, hasEquity: true,
		Debt: 95_304_000_000,
		Cash: 39_544_000_000,
	}
	got := valuationFields(320.19, in)

	// EBITDA over invested capital, which is equity plus debt less cash.
	roic, ok := got["return_on_invested_capital"].(float64)
	if !ok {
		t.Fatal("return_on_invested_capital is absent")
	}
	if diff := roic - 1.0350685938265556; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("return_on_invested_capital is %v, want Financial Datasets' 1.0350685938265556", roic)
	}

	// Apple pays no broken-out interest, so it has no coverage ratio.
	if _, present := got["interest_coverage"]; present {
		t.Fatal("interest_coverage was reported without an interest expense")
	}
	in.InterestExpense, in.hasInterest = 1_544_000_000, true
	in.EBIT = 18_688_000_000
	if coverage, _ := valuationFields(1, in)["interest_coverage"].(float64); coverage != 18_688_000_000.0/1_544_000_000.0 {
		t.Fatalf("interest_coverage is %v, want EBIT over interest expense", coverage)
	}
}

// TestEBITDAIsBuiltFromEBITAndAmortisation proves a trailing row no
// longer takes the provider's own single-quarter EBITDA line.
func TestEBITDAIsBuiltFromEBITAndAmortisation(t *testing.T) {
	income := map[string]any{
		"EBIT":   155_906_000_000.0,
		"EBITDA": 39_000_000_000.0, // one quarter, and so not to be used
	}
	cash := map[string]any{"Depreciation and Amortization": 13_100_000_000.0}

	in := inputsFor(income, map[string]any{}, cash, 0)
	if !in.hasEBITDA {
		t.Fatal("EBITDA was not derived")
	}
	if in.EBITDA != 169_006_000_000 {
		t.Fatalf("EBITDA is %v, want EBIT plus amortisation (169,006,000,000)", in.EBITDA)
	}

	// With no amortisation there is no EBITDA, rather than a quarter of one.
	if bare := inputsFor(income, map[string]any{}, map[string]any{}, 0); bare.hasEBITDA {
		t.Fatalf("EBITDA was reported as %v with no amortisation stated", bare.EBITDA)
	}
}
