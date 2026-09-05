package service

import (
	"math"
	"testing"
	"time"
)

// Apple's own figures, so the arithmetic is checkable by hand.
func TestValuationFields(t *testing.T) {
	in := valuationInputs{
		Shares: 14_800_000_000, hasShares: true,
		EPS: 8.75, hasEPS: true,
		Revenue: 466_823_000_000, hasRevenue: true,
		Equity: 107_520_000_000, hasEquity: true,
		FreeCashFlow: 98_767_000_000, hasFreeCashFlow: true,
		Debt: 98_000_000_000, Cash: 35_934_000_000,
		EPSGrowth: 0.32,
	}
	got := valuationFields(300.0, in)

	close := func(key string, want float64) {
		t.Helper()
		v, ok := got[key].(float64)
		if !ok {
			t.Fatalf("%s missing", key)
		}
		if math.Abs(v-want)/want > 0.001 {
			t.Fatalf("%s = %v, want about %v", key, v, want)
		}
	}
	close("market_cap", 300.0*14_800_000_000.0)
	close("price_to_earnings_ratio", 300.0/8.75)
	close("book_value_per_share", 107_520_000_000.0/14_800_000_000.0)
	close("price_to_sales_ratio", 300.0*14_800_000_000.0/466_823_000_000.0)
	close("enterprise_value", 300.0*14_800_000_000.0+98_000_000_000.0-35_934_000_000.0)
	close("free_cash_flow_yield", 98_767_000_000.0/(300.0*14_800_000_000.0))
	close("peg_ratio", (300.0/8.75)/(0.32*100))

	// EBITDA was not stated, so its multiple must be absent rather than zero.
	if _, present := got["enterprise_value_to_ebitda_ratio"]; present {
		t.Fatalf("an unstated EBITDA must not produce a multiple")
	}
}

// A loss-making quarter has no meaningful P/E, and a company with no
// stated share count has no market cap. Neither may be reported as zero.
func TestValuationOmitsWhatItCannotDerive(t *testing.T) {
	got := valuationFields(300.0, valuationInputs{EPS: -1.5, hasEPS: true})
	for _, key := range []string{"price_to_earnings_ratio", "market_cap", "enterprise_value"} {
		if _, present := got[key]; present {
			t.Fatalf("%s must be absent when it cannot be derived: %#v", key, got)
		}
	}
}

// A report period lands on a weekend often enough that an exact-date
// lookup would find no close at all.
func TestCloseOnOrBeforeWalksBackToTheLastTradingDay(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse(dateLayout, s); return d }
	closes := closesByDate{"2025-09-26": 255.5, "2025-09-29": 260.0}
	days := []time.Time{day("2025-09-26"), day("2025-09-29")}

	if got, ok := closeOnOrBefore(closes, days, day("2025-09-27")); !ok || got != 255.5 {
		t.Fatalf("Saturday close = %v (%v), want Friday's 255.5", got, ok)
	}
	if _, ok := closeOnOrBefore(closes, days, day("2025-01-01")); ok {
		t.Fatalf("a date before the series must yield nothing")
	}
}
