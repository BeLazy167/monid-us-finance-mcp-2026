package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ttlFor is the TTL the cache would give one request URL.
func ttlFor(t *testing.T, target string, now time.Time) time.Duration {
	t.Helper()
	return ttlForRequest(httptest.NewRequest(http.MethodGet, target, nil), now)
}

// TestTTL_SettledPeriodsOutliveTheLatest pins the rule the whole change
// rests on: an answer about a period that has closed cannot change, so
// expiring it in ten minutes only makes the next caller pay a provider
// to be told the same thing.
func TestTTL_SettledPeriodsOutliveTheLatest(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	settled := []string{
		"/financials/income-statements?ticker=AAPL&period=annual&report_period=2025-09-27",
		"/prices?ticker=AAPL&interval=day&start_date=2026-08-01&end_date=2026-08-31",
		"/filings/items?ticker=AAPL&filing_type=10-K&year=2025&item=Item-1",
		"/filings/items?ticker=AAPL&filing_type=10-Q&accession_number=0000320193-26-000020",
		"/financial-metrics?ticker=AAPL&period=annual&report_period_lte=2025-01-01",
	}
	for _, target := range settled {
		if got := ttlFor(t, target, now); got != ttlSettled {
			t.Errorf("%s\n  cached for %v, want the settled %v", target, got, ttlSettled)
		}
	}

	live := []string{
		"/financial-metrics?ticker=AAPL&period=ttm&limit=20",
		"/prices/snapshot?ticker=AAPL",
		"/financials/income-statements?ticker=AAPL&period=annual&limit=4",
		"/news?ticker=AAPL&limit=2",
		// Bounded below only: the newest period is still in the answer.
		"/financials/income-statements?ticker=AAPL&period=annual&report_period_gte=2020-01-01",
		// Today is not yet closed.
		"/prices?ticker=AAPL&start_date=2026-09-06&end_date=2026-09-06",
		// A future bound settles nothing.
		"/financials/income-statements?ticker=AAPL&report_period_lte=2027-01-01",
	}
	for _, target := range live {
		if got := ttlFor(t, target, now); got == ttlSettled {
			t.Errorf("%s\n  was treated as settled; it can still change", target)
		}
	}
}

// TestTTL_TrailingRowsAreNeverSettled pins the exception. A ttm row is
// priced at the latest close whatever else the request asks for, so it
// carries a live figure even when every date bound points at the past.
// Caching one for a day would serve yesterday's market cap.
func TestTTL_TrailingRowsAreNeverSettled(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, target := range []string{
		"/financial-metrics?ticker=AAPL&period=ttm&report_period_lte=2025-01-01",
		"/financial-metrics?ticker=AAPL&period=TTM&accession_number=0000320193-26-000020",
	} {
		if got := ttlFor(t, target, now); got == ttlSettled {
			t.Errorf("%s\n  was cached for a day, but its price is today's", target)
		}
	}
	// The same request for a closed annual period is settled.
	annual := "/financial-metrics?ticker=AAPL&period=annual&report_period_lte=2025-01-01"
	if got := ttlFor(t, annual, now); got != ttlSettled {
		t.Errorf("%s\n  cached for %v, want the settled %v", annual, got, ttlSettled)
	}
}

// TestTTL_PathPolicyStillApplies checks the volatility policy is
// unchanged for everything that is not settled.
func TestTTL_PathPolicyStillApplies(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for target, want := range map[string]time.Duration{
		"/prices/snapshot?ticker=AAPL":              ttlPricesAndNews,
		"/news?ticker=AAPL":                         ttlPricesAndNews,
		"/financials/income-statements?ticker=AAPL": ttlFinancials,
		"/financial-metrics?ticker=AAPL":            ttlFinancials,
		"/macro/interest-rates?bank=FED":            ttlDefault,
	} {
		if got := ttlFor(t, target, now); got != want {
			t.Errorf("%s cached for %v, want %v", target, got, want)
		}
	}
}

// TestTTL_MalformedDatesDoNotSettle checks a date this server cannot
// parse leaves the request on its ordinary TTL rather than pinning a
// wrong answer for a day.
func TestTTL_MalformedDatesDoNotSettle(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	for _, target := range []string{
		"/prices?ticker=AAPL&end_date=yesterday",
		"/prices?ticker=AAPL&end_date=",
		"/filings/items?ticker=AAPL&year=not-a-year",
	} {
		if got := ttlFor(t, target, now); got == ttlSettled {
			t.Errorf("%s was settled on a date that cannot be read", target)
		}
	}
}
