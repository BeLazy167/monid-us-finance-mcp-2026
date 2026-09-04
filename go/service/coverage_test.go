package service

import (
	"context"
	"errors"
	"testing"

	"github.com/belazy/monid-finance/providers"
)

// coverageCatalog exercises catalogTickerUniverse's dedup/sort/country
// filter: duplicate tickers (different case), a non-US record that must
// be dropped, and an out-of-order ticker set so ascending sort is a real
// assertion, not a no-op.
var coverageCatalog = map[string]any{
	"data": []any{
		map[string]any{"ticker": "MSFT", "companyName": "Microsoft Corp.", "country": "US", "countryName": "United States"},
		map[string]any{"ticker": "aapl", "companyName": "Apple Inc.", "country": "US", "countryName": "United States"},
		map[string]any{"ticker": "AAPL", "companyName": "Apple Inc.", "country": "US", "countryName": "United States"},
		map[string]any{"ticker": "GOOG", "companyName": "Alphabet Inc.", "country": "US", "countryName": "United States"},
		map[string]any{"ticker": "SHOP", "companyName": "Shopify Inc.", "country": "CA", "countryName": "Canada"},
	},
}

func coverageOutcomes() map[string]fakeOutcome {
	return map[string]fakeOutcome{
		"defillama /equities/v1/companies-list": {output: coverageCatalog},
	}
}

// --- coverage lists: one catalog fetch serves all eight ---

func TestCoverage_OneCatalogFetchServesEveryList(t *testing.T) {
	svc, transport := newTestService(t, coverageOutcomes())
	calls := []func() (Result, error){
		func() (Result, error) {
			return svc.ListCompanyFactsTickers(context.Background(), "key", map[string]any{})
		},
		func() (Result, error) { return svc.ListEarningsTickers(context.Background(), "key", map[string]any{}) },
		func() (Result, error) { return svc.ListFilingsTickers(context.Background(), "key", map[string]any{}) },
		func() (Result, error) {
			return svc.ListMetricsSnapshotTickers(context.Background(), "key", map[string]any{})
		},
		func() (Result, error) { return svc.ListPricesTickers(context.Background(), "key", map[string]any{}) },
		func() (Result, error) {
			return svc.ListPriceSnapshotTickers(context.Background(), "key", map[string]any{})
		},
		func() (Result, error) {
			return svc.ListInstitutionalHoldingsTickers(context.Background(), "key", map[string]any{})
		},
		func() (Result, error) { return svc.ListKPITickers(context.Background(), "key", map[string]any{}) },
	}
	for i, call := range calls {
		if _, err := call(); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}
	if transport.CallCount() != 1 {
		t.Fatalf("expected exactly 1 catalog fetch shared across all 8 coverage lists, got %d", transport.CallCount())
	}
}

// --- sorted ascending, deduped, country-filtered, and limit-capped ---

func TestCoverage_SortedDedupedAndLimited(t *testing.T) {
	svc, transport := newTestService(t, coverageOutcomes())
	result, err := svc.ListCompanyFactsTickers(context.Background(), "key", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	if body["resource"] != "company_facts" {
		t.Fatalf("expected resource=company_facts, got %v", body["resource"])
	}
	if body["total"] != float64(3) {
		t.Fatalf("expected total=3 (AAPL/GOOG/MSFT deduped, SHOP dropped as non-US), got %v", body["total"])
	}
	tickers, ok := body["tickers"].([]any)
	if !ok {
		t.Fatalf("tickers is not an array: %#v", body["tickers"])
	}
	got := make([]string, len(tickers))
	for i, v := range tickers {
		got[i], _ = v.(string)
	}
	want := []string{"AAPL", "GOOG", "MSFT"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected ascending sorted %v, got %v", want, got)
		}
	}

	limited, err := svc.ListCompanyFactsTickers(context.Background(), "key", map[string]any{"limit": 1.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	limitedBody := jsonRoundTrip(t, limited.Value)
	limitedTickers, _ := limitedBody["tickers"].([]any)
	if len(limitedTickers) != 1 || limitedTickers[0] != "AAPL" {
		t.Fatalf("expected limit=1 to return just [AAPL], got %v", limitedTickers)
	}
	if limitedBody["total"] != float64(3) {
		t.Fatalf("expected total to stay the full universe size (3) regardless of limit, got %v", limitedBody["total"])
	}
	if transport.CallCount() != 1 {
		t.Fatalf("expected the second call to be served from cache, got %d transport calls", transport.CallCount())
	}
}

// --- static lists: zero transport calls ---

func TestCoverage_StaticListsAreZeroCost(t *testing.T) {
	svc, transport := newTestService(t, map[string]fakeOutcome{})

	filingTypes, err := svc.ListFilingTypes(context.Background(), "key", map[string]any{})
	if err != nil {
		t.Fatalf("list_filing_types: unexpected error: %v", err)
	}
	ftBody := jsonRoundTrip(t, filingTypes.Value)
	ft, _ := ftBody["filing_types"].([]any)
	// The catalog is generated from SEC EDGAR's quarterly form indexes,
	// so this asserts its contract rather than a fixed count: it must be
	// sorted, must exactly match what get_filings accepts, and must carry
	// the common forms a caller will actually filter on.
	if len(ft) != len(validFilingTypeEnum) {
		t.Fatalf("published catalog has %d entries but get_filings accepts %d; the two must not drift",
			len(ft), len(validFilingTypeEnum))
	}
	prevType := ""
	published := make(map[string]bool, len(ft))
	for _, item := range ft {
		code, ok := item.(string)
		if !ok {
			t.Fatalf("expected a form-type string, got %#v", item)
		}
		if !validFilingTypeEnum[code] {
			t.Fatalf("catalog advertises %q but get_filings would reject it", code)
		}
		if code < prevType {
			t.Fatalf("filing types must be sorted, got %q after %q", code, prevType)
		}
		prevType = code
		published[code] = true
	}
	for _, want := range []string{"10-K", "10-Q", "8-K", "20-F", "6-K", "S-1", "DEF 14A", "SC 13D/A", "424B4"} {
		if !published[want] {
			t.Fatalf("catalog is missing %q, a form type callers filter on in practice", want)
		}
	}

	banks, err := svc.ListInterestRateBanks(context.Background(), "key", map[string]any{})
	if err != nil {
		t.Fatalf("list_interest_rate_banks: unexpected error: %v", err)
	}
	banksBody := jsonRoundTrip(t, banks.Value)
	bankList, _ := banksBody["banks"].([]any)
	if len(bankList) != len(bankSpecs) {
		t.Fatalf("expected %d banks (derived from bankSpecs), got %d", len(bankSpecs), len(bankList))
	}
	// Financial Datasets answers a flat, sorted array of bank codes here
	// (verified live 2026-09-04), not objects. Codes must still all come
	// from bankSpecs, so this list cannot claim a bank we never scrape.
	if banksBody["resource"] != "interest_rates" {
		t.Fatalf("resource = %v, want interest_rates", banksBody["resource"])
	}
	known := make(map[string]bool, len(bankSpecs))
	for _, spec := range bankSpecs {
		known[spec.Bank] = true
	}
	prev := ""
	for _, item := range bankList {
		code, ok := item.(string)
		if !ok {
			t.Fatalf("expected a bare bank code string, got %#v", item)
		}
		if !known[code] {
			t.Fatalf("bank %q is not in bankSpecs; this route must never claim a bank we do not scrape", code)
		}
		if code < prev {
			t.Fatalf("bank codes must be sorted, got %q after %q", code, prev)
		}
		prev = code
	}

	itemTypes, err := svc.ListFilingItemTypes(context.Background(), "key", map[string]any{})
	if err != nil {
		t.Fatalf("list_filing_item_types: unexpected error: %v", err)
	}
	if itemTypes.Value == nil {
		t.Fatalf("expected a non-nil filing item type catalog")
	}

	if transport.CallCount() != 0 {
		t.Fatalf("expected zero transport calls for static catalogs, got %d", transport.CallCount())
	}
}

// --- get_all_financials ---

func TestAllFinancials_ComposesAllThreeConcurrently(t *testing.T) {
	svc, transport := newTestService(t, fullOutcomes())
	result, err := svc.GetAllFinancials(context.Background(), "key", map[string]any{"ticker": "AAPL", "period": "annual", "limit": 2.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// income and balance both come out of the SAME /equities/v1/statements
	// payload, so composing them must not double-fetch it. The third call
	// is marketbeat: that feed's cash flow subtotals disagree with SEC on
	// three of four lines, so the cash flow statement is sourced there
	// instead (see marketbeatcashflow.go). Paying one extra run beats
	// answering this route with a number measured wrong 8 times out of 8,
	// and beats the alternative that shipped briefly, where this route
	// and get_cash_flow_statement returned different values for the same
	// field.
	if transport.CallCount() != 3 {
		t.Fatalf("expected 3 calls (statements + filings + marketbeat cash flow), got %d",
			transport.CallCount())
	}
	body := jsonRoundTrip(t, result.Value)
	financials, ok := body["financials"].(map[string]any)
	if !ok {
		t.Fatalf("expected a financials object, got %#v", body["financials"])
	}
	income, _ := financials["income_statements"].([]any)
	balance, _ := financials["balance_sheets"].([]any)
	cash, _ := financials["cash_flow_statements"].([]any)
	if len(income) != 2 || len(balance) != 2 || len(cash) != 2 {
		t.Fatalf("expected 2 annual records per statement kind (limit=2), got income=%d balance=%d cash=%d",
			len(income), len(balance), len(cash))
	}
	row := income[0].(map[string]any)
	if row["ticker"] != "AAPL" || row["period"] != "annual" {
		t.Fatalf("unexpected income row shape: %v", row)
	}
}

func TestAllFinancials_RejectsAsReportedBeforeAnyCall(t *testing.T) {
	svc, transport := newTestService(t, map[string]fakeOutcome{})
	_, err := svc.GetAllFinancials(context.Background(), "key", map[string]any{"ticker": "AAPL", "as_reported": true})
	if err == nil {
		t.Fatalf("expected an error")
	}
	var inputErr *providers.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected *providers.InputError, got %T: %v", err, err)
	}
	if transport.CallCount() != 0 {
		t.Fatalf("expected zero transport calls, got %d", transport.CallCount())
	}
}

// --- search_line_items ---

func TestSearchLineItems_ValidationBeforeAnyCall(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"missing line_items", map[string]any{"tickers": []any{"AAPL"}}},
		{"empty line_items", map[string]any{"tickers": []any{"AAPL"}, "line_items": []any{}}},
		{"missing tickers", map[string]any{"line_items": []any{"revenue"}}},
		{"empty tickers", map[string]any{"line_items": []any{"revenue"}, "tickers": []any{}}},
		{"too many tickers", map[string]any{
			"line_items": []any{"revenue"},
			"tickers":    []any{"A", "B", "C", "D", "E", "F"},
		}},
		{"duplicate tickers", map[string]any{
			"line_items": []any{"revenue"},
			"tickers":    []any{"AAPL", "aapl"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, transport := newTestService(t, map[string]fakeOutcome{})
			_, err := svc.SearchLineItems(context.Background(), "key", tc.args)
			if err == nil {
				t.Fatalf("expected an error")
			}
			var inputErr *providers.InputError
			if !errors.As(err, &inputErr) {
				t.Fatalf("expected *providers.InputError, got %T: %v", err, err)
			}
			if transport.CallCount() != 0 {
				t.Fatalf("expected zero transport calls, got %d", transport.CallCount())
			}
		})
	}
}

func TestSearchLineItems_FetchesTickersConcurrentlyAndShapesResults(t *testing.T) {
	svc, transport := newTestService(t, fullOutcomes())
	result, err := svc.SearchLineItems(context.Background(), "key", map[string]any{
		"line_items": []any{"revenue", "total_assets", "not_a_real_field"},
		"tickers":    []any{"AAPL", "MSFT"},
		"period":     "annual",
		"limit":      1.0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.WrapperKey != "search_results" {
		t.Fatalf("expected WrapperKey=search_results, got %q", result.WrapperKey)
	}
	// fullOutcomes registers one canned statements outcome per endpoint
	// (fakeTransport keys on provider+endpoint only), so both tickers'
	// concurrent fetches land on the same outcome; MSFT is still a
	// distinct cache key from AAPL, so both are real, separate calls.
	if transport.CallCount() != 2 {
		t.Fatalf("expected 2 concurrent statements calls (one per ticker), got %d", transport.CallCount())
	}
	records := asRecords(t, result.Value)
	if len(records) != 2 {
		t.Fatalf("expected 1 result row per ticker (limit=1), got %d", len(records))
	}
	row := jsonRoundTrip(t, records[0])
	if row["ticker"] != "AAPL" {
		t.Fatalf("expected the first row for AAPL (requested first), got %v", row["ticker"])
	}
	if _, hasRevenue := row["revenue"]; !hasRevenue {
		t.Fatalf("expected revenue to be present (income statement field), got %v", row)
	}
	if _, hasAssets := row["total_assets"]; !hasAssets {
		t.Fatalf("expected total_assets to be present (balance sheet field), got %v", row)
	}
	if _, hasFake := row["not_a_real_field"]; hasFake {
		t.Fatalf("expected an unmatched line item to be silently omitted, got %v", row)
	}
	if _, hasCurrency := row["currency"]; hasCurrency {
		t.Fatalf("expected currency to stay omitted (never fabricated), got %v", row)
	}
}
