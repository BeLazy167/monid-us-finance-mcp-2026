package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/monid"
	"github.com/belazy/monid-finance/providers"
)

// --- shared fixtures, adapted 1:1 from tests/test_service.py so behavior
// stays directly comparable to the Python reference implementation. ---

var testCatalog = map[string]any{
	"data": []any{
		map[string]any{"ticker": "AAPL", "companyName": "Apple Inc.", "country": "US"},
	},
}

var testSummary = map[string]any{
	"currentPrice":             230.1,
	"marketCap":                3_400_000_000_000.0,
	"trailingPE":               34.2,
	"priceToBook":              55.0,
	"priceToRevenue":           8.9,
	"enterpriseValueToEbitda":  27.5,
	"priceChange1d":            1.2,
	"priceChangePercentage1d":  0.5,
	"revenueTTM":               400.0,
	"grossProfitTTM":           180.0,
	"earningsTTM":              100.0,
	"operatingProfitMarginTTM": 0.3,
	"updatedAt":                "2026-09-03T20:00:00Z",
}

var testAnnualDates = []any{"2024-12-31", "2025-12-31"}
var testQuarterlyDates = []any{"2025-06-30", "2025-09-30", "2025-12-31", "2026-03-31"}

var testStatements = map[string]any{
	"incomeStatement": map[string]any{
		"labels": []any{"Revenue", "Cost of Revenue", "Gross Profit", "Operating Income",
			"Income Tax", "Net Income", "EPS (Basic)", "EPS (Diluted)",
			"Shares Outstanding (Basic)", "EBIT"},
		"annual": map[string]any{
			"periodEnding": testAnnualDates,
			"values": []any{
				[]any{100.0, 120.0},
				[]any{60.0, 70.0},
				[]any{40.0, 50.0},
				[]any{30.0, 35.0},
				[]any{6.0, 7.0},
				[]any{24.0, 28.0},
				[]any{2.0, 2.1},
				[]any{1.9, 2.0},
				[]any{12.0, 13.0},
				[]any{31.0, 36.0},
			},
			"children": map[string]any{
				"Non-Operating Items": map[string]any{"values": []any{[]any{3.0, 4.0}}},
			},
		},
		"quarterly": map[string]any{
			"periodEnding": testQuarterlyDates,
			"values": []any{
				[]any{20.0, 22.0, 24.0, 25.0},
				[]any{12.0, 13.0, 14.0, 15.0},
				[]any{8.0, 9.0, 10.0, 10.0},
				[]any{6.0, 7.0, 8.0, 8.0},
				[]any{1.0, 1.0, 2.0, 2.0},
				[]any{5.0, 6.0, 6.0, 6.0},
				[]any{0.5, 0.6, 0.5, 0.5},
				[]any{0.5, 0.5, 0.5, 0.5},
				[]any{10.0, 10.0, 11.0, 12.0},
				[]any{6.0, 7.0, 8.0, 8.0},
			},
			"children": map[string]any{
				"Non-Operating Items": map[string]any{"values": []any{[]any{1.0, 1.0, 2.0, 2.0}}},
			},
		},
		"children": map[string]any{
			"annual":    map[string]any{"Non-Operating Items": map[string]any{"labels": []any{"Non-Operating Interest Expense"}}},
			"quarterly": map[string]any{"Non-Operating Items": map[string]any{"labels": []any{"Non-Operating Interest Expense"}}},
		},
	},
	"balanceSheet": map[string]any{
		"labels": []any{"Total Assets", "Total Current Assets", "Total Liabilities", "Total Shareholders Equity"},
		"annual": map[string]any{
			"periodEnding": testAnnualDates,
			"values": []any{
				[]any{400.0, 420.0},
				[]any{200.0, 210.0},
				[]any{250.0, 260.0},
				[]any{150.0, 160.0},
			},
		},
		"quarterly": map[string]any{
			"periodEnding": testQuarterlyDates,
			"values": []any{
				[]any{405.0, 410.0, 415.0, 420.0},
				[]any{202.0, 206.0, 208.0, 210.0},
				[]any{252.0, 255.0, 258.0, 260.0},
				[]any{153.0, 155.0, 157.0, 160.0},
			},
		},
		"children": map[string]any{},
	},
	"cashflow": map[string]any{
		"labels": []any{"Cash Flow from Operating Activities", "Free Cash Flow", "Net Cash Flow", "End Cash Position", "Net Income"},
		"annual": map[string]any{
			"periodEnding": testAnnualDates,
			"values": []any{
				[]any{60.0, 70.0},
				[]any{50.0, 60.0},
				[]any{10.0, 11.0},
				[]any{30.0, 33.0},
				[]any{24.0, 28.0},
			},
		},
		"quarterly": map[string]any{
			"periodEnding": testQuarterlyDates,
			"values": []any{
				[]any{14.0, 16.0, 18.0, 19.0},
				[]any{12.0, 13.0, 15.0, 16.0},
				[]any{2.0, 3.0, 3.0, 4.0},
				[]any{31.0, 32.0, 33.0, 33.0},
				[]any{5.0, 6.0, 6.0, 6.0},
			},
		},
		"children": map[string]any{},
	},
}

var testFilings = []any{
	map[string]any{
		"filingDate": "2026-02-01", "reportDate": "2025-12-31", "form": "10-K",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20241231.htm",
	},
	map[string]any{
		"filingDate": "2026-01-15", "reportDate": "2025-12-31", "form": "8-K",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019326000001/a.htm",
	},
	map[string]any{
		"filingDate": "2025-11-01", "reportDate": "2025-09-30", "form": "10-Q",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250930.htm",
	},
}

var testOHLCV = []any{
	[]any{1767225600.0, 30.0, 34.0, 29.0, 33.0, 300.0},
	[]any{1767139200.0, 20.0, 24.0, 19.0, 23.0, 200.0},
	[]any{1767052800.0, 10.0, 14.0, 9.0, 13.0, 100.0},
}

var testNews = map[string]any{
	"data": []any{
		map[string]any{"id": "old", "title": "Old", "published_at": "2026-01-01T12:00:00Z", "url": "https://example.com/old", "source": "example.com"},
		map[string]any{"id": "new", "title": "New", "published_at": "2026-02-01T12:00:00Z", "url": "https://example.com/new", "source": "example.com"},
	},
}

func fullOutcomes() map[string]fakeOutcome {
	return map[string]fakeOutcome{
		"defillama /equities/v1/companies-list": {output: testCatalog},
		"defillama /equities/v1/summary":        {output: testSummary},
		"defillama /equities/v1/statements":     {output: testStatements},
		"defillama /equities/v1/filings":        {output: testFilings},
		"defillama /equities/v1/ohlcv":          {output: testOHLCV},
		"context.dev /news/search":              {output: testNews},
	}
}

func TestDefaults_IncomeStatementPeriodAndLimit(t *testing.T) {
	svc, transport := newTestService(t, fullOutcomes())
	// No period/limit supplied: the FD JSON Schema default is period=ttm,
	// limit=4 (docs/fd-mcp-tool-schemas.json), even though the Python
	// source's own internal default differs (period="annual") - this port
	// follows the schema, per its brief.
	result, err := svc.Call(context.Background(), "key", "get_income_statement", map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.WrapperKey != "income_statements" || !result.Paginate {
		t.Fatalf("unexpected envelope: wrapperKey=%q paginate=%v", result.WrapperKey, result.Paginate)
	}
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 ttm record (4 consecutive quarters -> 1 window), got %d", len(records))
	}
	row := jsonRoundTrip(t, records[0])
	if row["period"] != "ttm" {
		t.Fatalf("expected default period ttm, got %v", row["period"])
	}
	if transport.CallCount() != 2 {
		t.Fatalf("expected 2 calls (statements + filings), got %d", transport.CallCount())
	}
}

func TestDefaults_GetFilingsLimit(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_filings", map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 3 {
		t.Fatalf("expected all 3 fixture filings (limit default 100 >= 3), got %d", len(records))
	}
}

func TestValidationBeforeCall_ZeroTransportCalls(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"missing ticker", "get_income_statement", map[string]any{}},
		{"bad ticker", "get_stock_price", map[string]any{"ticker": "bad ticker!!"}},
		{"cik rejected", "get_company_facts", map[string]any{"cik": "0000320193"}},
		{"as_reported rejected", "get_income_statement", map[string]any{"ticker": "AAPL", "as_reported": true}},
		{"interval_multiplier rejected", "get_stock_prices", map[string]any{"ticker": "AAPL", "interval_multiplier": 2.0}},
		{"currency rejected", "screen_stocks", map[string]any{"filters": []any{}, "currency": "EUR"}},
		{"insider form_type rejected", "get_insider_trades", map[string]any{"ticker": "AAPL", "form_type": "4"}},
		{"screen_stocks missing filters", "screen_stocks", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, transport := newTestService(t, map[string]fakeOutcome{})
			_, err := svc.Call(context.Background(), "key", tc.tool, tc.args)
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

func TestOwnershipTools_ZeroCostUnsupported(t *testing.T) {
	tools := []string{
		"get_beneficial_owners", "get_beneficial_ownership",
		"get_insider_ownership", "get_institutional_investors",
	}
	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			svc, transport := newTestService(t, map[string]fakeOutcome{})
			_, err := svc.Call(context.Background(), "key", tool, map[string]any{"ticker": "AAPL"})
			var unsupported *providers.UnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("expected *providers.UnsupportedError, got %T: %v", err, err)
			}
			if transport.CallCount() != 0 {
				t.Fatalf("expected zero transport calls, got %d", transport.CallCount())
			}
		})
	}
}

func TestCacheHit_NoSecondCallAndNoLedgerRow(t *testing.T) {
	transport := newFakeTransport(t, fullOutcomes())
	httpClient := &http.Client{Transport: transport}
	ledgerPath := filepath.Join(t.TempDir(), "ledger.jsonl")
	ledger := fd.NewReceiptsLedger(ledgerPath)
	t.Cleanup(func() { _ = ledger.Close() })
	svc := New(Config{HTTP: httpClient, Allowlist: allowAll{}, MaxConcurrentRuns: 8, Ledger: ledger})

	if _, err := svc.Call(context.Background(), "key", "get_stock_price", map[string]any{"ticker": "AAPL"}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := svc.Call(context.Background(), "key", "get_stock_price", map[string]any{"ticker": "AAPL"}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if transport.CallCount() != 1 {
		t.Fatalf("expected exactly 1 transport call (second served from cache), got %d", transport.CallCount())
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("close ledger: %v", err)
	}
	raw, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	lineCount := 0
	for _, b := range raw {
		if b == '\n' {
			lineCount++
		}
	}
	if lineCount != 1 {
		t.Fatalf("expected exactly 1 ledger row (cache hit writes none), got %d", lineCount)
	}
}

func TestParallelFanOut_StatementsAndFilingsBothCalled(t *testing.T) {
	svc, transport := newTestService(t, fullOutcomes())
	if _, err := svc.Call(context.Background(), "key", "get_financial_metrics", map[string]any{"ticker": "AAPL"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range transport.Calls() {
		seen[c.Provider+" "+c.Endpoint] = true
	}
	if !seen["defillama /equities/v1/statements"] || !seen["defillama /equities/v1/filings"] {
		t.Fatalf("expected both statements and filings calls, got %v", transport.Calls())
	}
}

func TestParallelFanOut_InterestRatesAllFourBanks(t *testing.T) {
	// fakeTransport keys purely on provider+endpoint (not the URL query
	// param), so all four bank scrapes share one canned outcome; its
	// returned "url" cannot match every bank's expected URL, so parsing
	// fails and every bank is silently omitted (matching Python's own
	// `except (UpstreamError, SchemaDriftError): continue`). That is
	// still enough to prove the fan-out issues all four calls.
	outcomes := map[string]fakeOutcome{
		"context.dev /web/scrape/markdown": {output: map[string]any{
			"success": true, "url": "https://example.com/placeholder",
			"markdown":      "federal funds rate 4.25 to 4.50 percent as of January 1, 2026",
			"contentLength": 5,
		}},
	}
	svc, transport := newTestService(t, outcomes)
	_, err := svc.Call(context.Background(), "key", "get_interest_rates", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport.CallCount() != len(bankSpecs) {
		t.Fatalf("expected %d bank scrape calls, got %d", len(bankSpecs), transport.CallCount())
	}
}

func TestErrorClassification(t *testing.T) {
	t.Run("unauthorized", func(t *testing.T) {
		outcomes := map[string]fakeOutcome{
			"defillama /equities/v1/summary": {httpStatus: 401},
		}
		svc, _ := newTestService(t, outcomes)
		_, err := svc.Call(context.Background(), "key", "get_stock_price", map[string]any{"ticker": "AAPL"})
		var runErr *monid.RunError
		if !errors.As(err, &runErr) || !errors.Is(runErr, monid.ErrUnauthorized) {
			t.Fatalf("expected monid.ErrUnauthorized, got %T: %v", err, err)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		outcomes := map[string]fakeOutcome{
			"defillama /equities/v1/summary": {httpStatus: 402},
		}
		svc, _ := newTestService(t, outcomes)
		_, err := svc.Call(context.Background(), "key", "get_stock_price", map[string]any{"ticker": "AAPL"})
		var runErr *monid.RunError
		if !errors.As(err, &runErr) || !errors.Is(runErr, monid.ErrBlocked) {
			t.Fatalf("expected monid.ErrBlocked, got %T: %v", err, err)
		}
	})
	t.Run("provider http error maps to upstream_error class", func(t *testing.T) {
		outcomes := map[string]fakeOutcome{
			"defillama /equities/v1/summary": {httpStatus: 503},
		}
		svc, _ := newTestService(t, outcomes)
		_, err := svc.Call(context.Background(), "key", "get_stock_price", map[string]any{"ticker": "AAPL"})
		var runErr *monid.RunError
		if !errors.As(err, &runErr) || !errors.Is(runErr, monid.ErrProviderHTTP) {
			t.Fatalf("expected monid.ErrProviderHTTP, got %T: %v", err, err)
		}
	})
	t.Run("schema drift on unparseable payload", func(t *testing.T) {
		outcomes := map[string]fakeOutcome{
			"defillama /equities/v1/summary": {output: map[string]any{"nothing_recognized": true}},
		}
		svc, _ := newTestService(t, outcomes)
		_, err := svc.Call(context.Background(), "key", "get_stock_price", map[string]any{"ticker": "AAPL"})
		var schemaErr *providers.SchemaDriftError
		if !errors.As(err, &schemaErr) {
			t.Fatalf("expected *providers.SchemaDriftError, got %T: %v", err, err)
		}
	})
}

// --- FD shape checks (one per tool family) ---

func TestFDShape_CompanyFacts(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_company_facts", map[string]any{"ticker": "aapl"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	facts, ok := body["company_facts"].(map[string]any)
	if !ok {
		t.Fatalf("expected company_facts object, got %#v", body)
	}
	if facts["ticker"] != "AAPL" || facts["name"] != "Apple Inc." {
		t.Fatalf("unexpected company_facts: %#v", facts)
	}

	notFound, err := svc.Call(context.Background(), "key", "get_company_facts", map[string]any{"ticker": "ZZZZ"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nf := jsonRoundTrip(t, notFound.Value)
	if nf["error"] != "not_found" {
		t.Fatalf("expected not_found, got %#v", nf)
	}
}

func TestFDShape_IncomeStatementAnnual(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_income_statement",
		map[string]any{"ticker": "AAPL", "period": "annual", "limit": 4.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 2 {
		t.Fatalf("expected 2 annual records, got %d", len(records))
	}
	newest := jsonRoundTrip(t, records[0])
	want := map[string]any{
		"report_period": "2025-12-31", "fiscal_period": "FY2025", "period": "annual", "ticker": "AAPL",
		"revenue": 120.0, "cost_of_revenue": 70.0, "gross_profit": 50.0, "operating_income": 35.0,
		"income_tax_expense": 7.0, "net_income": 28.0, "interest_expense": 4.0, "ebit": 36.0,
		"earnings_per_share": 2.1, "earnings_per_share_diluted": 2.0, "weighted_average_shares": 13.0,
		"accession_number": "0000320193-25-000079", "form_type": "10-K", "filing_date": "2026-02-01",
	}
	for key, expected := range want {
		if newest[key] != expected {
			t.Fatalf("field %s: got %#v want %#v (record=%#v)", key, newest[key], expected, newest)
		}
	}
	if _, hasNext := newest["next_page_url"]; hasNext {
		t.Fatalf("record itself should not carry next_page_url")
	}
}

func TestFDShape_IncomeStatementTTM(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_income_statement",
		map[string]any{"ticker": "AAPL", "period": "ttm"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 ttm record, got %d", len(records))
	}
	ttm := jsonRoundTrip(t, records[0])
	if ttm["report_period"] != "2026-03-31" || ttm["period"] != "ttm" {
		t.Fatalf("unexpected ttm record: %#v", ttm)
	}
	if ttm["revenue"] != 91.0 {
		t.Fatalf("expected summed ttm revenue 91, got %v", ttm["revenue"])
	}
	if ttm["net_income"] != 23.0 {
		t.Fatalf("expected summed ttm net_income 23, got %v", ttm["net_income"])
	}
	if _, has := ttm["fiscal_period"]; has {
		t.Fatalf("ttm records must omit fiscal_period")
	}
}

func TestFDShape_BalanceSheetAndCashFlow(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	balance, err := svc.Call(context.Background(), "key", "get_balance_sheet", map[string]any{"ticker": "AAPL", "period": "annual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, balance.Value)
	newest := jsonRoundTrip(t, records[0])
	if newest["total_assets"] != 420.0 || newest["current_assets"] != 210.0 || newest["shareholders_equity"] != 160.0 {
		t.Fatalf("unexpected balance sheet record: %#v", newest)
	}

	cash, err := svc.Call(context.Background(), "key", "get_cash_flow_statement", map[string]any{"ticker": "AAPL", "period": "annual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records = asRecords(t, cash.Value)
	newest = jsonRoundTrip(t, records[0])
	if newest["net_cash_flow_from_operations"] != 70.0 || newest["free_cash_flow"] != 60.0 || newest["ending_cash_balance"] != 33.0 {
		t.Fatalf("unexpected cash flow record: %#v", newest)
	}
}

func TestFDShape_FinancialMetricsSnapshot(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_financial_metrics_snapshot", map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	snapshot, ok := body["snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("expected snapshot object, got %#v", body)
	}
	if snapshot["ticker"] != "AAPL" || snapshot["market_cap"] != 3_400_000_000_000.0 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if gm, _ := snapshot["gross_margin"].(float64); gm < 0.449 || gm > 0.451 {
		t.Fatalf("expected gross_margin ~0.45, got %v", snapshot["gross_margin"])
	}
}

func TestFDShape_StockPricesAggregation(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_stock_prices",
		map[string]any{"ticker": "AAPL", "interval": "day", "start_date": "2025-12-30", "end_date": "2026-01-01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 3 {
		t.Fatalf("expected 3 daily bars, got %d", len(records))
	}
	first := jsonRoundTrip(t, records[0])
	if first["time"] != "2025-12-30" {
		t.Fatalf("expected ascending order starting 2025-12-30, got %v", first["time"])
	}

	monthly, err := svc.Call(context.Background(), "key", "get_stock_prices",
		map[string]any{"ticker": "AAPL", "interval": "month", "start_date": "2025-12-30", "end_date": "2026-01-01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mRecords := asRecords(t, monthly.Value)
	if len(mRecords) != 2 {
		t.Fatalf("expected 2 monthly bars, got %d", len(mRecords))
	}
	december := jsonRoundTrip(t, mRecords[0])
	if december["time"] != "2025-12-31" || december["open"] != 10.0 || december["close"] != 23.0 || december["volume"] != 300.0 {
		t.Fatalf("unexpected december bar: %#v", december)
	}
}

func TestFDShape_StockPriceSnapshot(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_stock_price", map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	snapshot := body["snapshot"].(map[string]any)
	if snapshot["price"] != 230.1 || snapshot["ticker"] != "AAPL" || snapshot["day_change"] != 1.2 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestFDShape_News(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_news", map[string]any{"ticker": "AAPL", "limit": 5.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 2 {
		t.Fatalf("expected 2 news records, got %d", len(records))
	}
	newest := jsonRoundTrip(t, records[0])
	want := map[string]any{"ticker": "AAPL", "title": "New", "source": "example.com",
		"date": "2026-02-01T12:00:00Z", "url": "https://example.com/new"}
	for key, expected := range want {
		if newest[key] != expected {
			t.Fatalf("field %s: got %#v want %#v", key, newest[key], expected)
		}
	}

	_, err = svc.Call(context.Background(), "key", "get_news", map[string]any{})
	var inputErr *providers.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected InputError for missing ticker, got %v", err)
	}
}

func TestFDShape_FilingsFilterAndReject(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_filings",
		map[string]any{"ticker": "AAPL", "filing_type": []any{"10-K"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 10-K filing, got %d", len(records))
	}
	record := jsonRoundTrip(t, records[0])
	if record["filing_type"] != "10-K" || record["accession_number"] != "0000320193-25-000079" {
		t.Fatalf("unexpected filing record: %#v", record)
	}

	_, err = svc.Call(context.Background(), "key", "get_filings", map[string]any{"ticker": "AAPL", "filing_type": []any{"40-F"}})
	var inputErr *providers.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected InputError for invalid filing_type, got %v", err)
	}
}

func TestFDShape_FinancialMetrics(t *testing.T) {
	svc, _ := newTestService(t, fullOutcomes())
	result, err := svc.Call(context.Background(), "key", "get_financial_metrics",
		map[string]any{"ticker": "AAPL", "period": "annual", "limit": 4.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 2 {
		t.Fatalf("expected 2 annual metrics records, got %d", len(records))
	}
	newest := jsonRoundTrip(t, records[0])
	if newest["report_period"] != "2025-12-31" || newest["fiscal_period"] != "FY2025" {
		t.Fatalf("unexpected metrics record: %#v", newest)
	}
	if gm, _ := newest["gross_margin"].(float64); gm < 0.415 || gm > 0.418 {
		t.Fatalf("expected gross_margin ~0.4167 (50/120), got %v", newest["gross_margin"])
	}
	if newest["accession_number"] != "0000320193-25-000079" || newest["form_type"] != "10-K" {
		t.Fatalf("expected identity join, got %#v", newest)
	}

	ttm, err := svc.Call(context.Background(), "key", "get_financial_metrics", map[string]any{"ticker": "AAPL", "period": "ttm"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ttmRecords := asRecords(t, ttm.Value)
	if len(ttmRecords) == 0 {
		t.Fatalf("expected at least one ttm metrics record")
	}
	ttmRow := jsonRoundTrip(t, ttmRecords[0])
	for _, key := range []string{"accession_number", "form_type", "filing_url", "filing_date"} {
		if _, has := ttmRow[key]; has {
			t.Fatalf("ttm metrics record must omit %s, got %#v", key, ttmRow)
		}
	}
}

func TestFDShape_InsiderTradesAndScreener(t *testing.T) {
	insiderPayload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"query": "AAPL",
			"results": []any{
				map[string]any{
					"transaction_date": "2026-09-01 Sale", "reported_datetime": "2026-09-03 6:30 pm",
					"company": "Apple Inc.", "symbol": "AAPL", "insider_relationship": "Newstead Jennifer SVP, GC",
					"shares_traded": "1,439", "average_price": "$317.01", "total_amount": "$456,177",
					"shares_owned": "35,790 (Direct)",
				},
			},
		},
	}
	screenerPayload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"data": map[string]any{
				"filters": map[string]any{},
				"table": map[string]any{
					"asOf": nil,
					"headers": map[string]any{
						"symbol": "Symbol", "name": "Name", "lastsale": "Last Sale",
						"netchange": "Net Change", "pctchange": "% Change", "marketCap": "Market Cap",
					},
					"rows": []any{
						map[string]any{
							"symbol": "AAPL", "name": "Apple Inc. Common Stock", "lastsale": "$328.21",
							"netchange": "-3.25", "pctchange": "-1.00%", "marketCap": "4,789,955,817,800",
							"url": "/market-activity/stocks/aapl",
						},
					},
				},
				"totalrecords": 33.0,
				"asof":         "Last price as of Sep 3, 2026",
			},
			"message": nil,
			"status":  map[string]any{"rCode": 200.0, "bCodeMessage": nil, "developerMessage": nil},
		},
	}
	outcomes := map[string]fakeOutcome{
		"secform4 /search":           {output: insiderPayload},
		"nasdaq /get_stock_screener": {output: screenerPayload},
	}
	svc, _ := newTestService(t, outcomes)

	trades, err := svc.Call(context.Background(), "key", "get_insider_trades", map[string]any{"ticker": "AAPL", "limit": 10.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tradeRecords := asRecords(t, trades.Value)
	if len(tradeRecords) != 1 {
		t.Fatalf("expected 1 insider trade, got %d", len(tradeRecords))
	}
	trade := jsonRoundTrip(t, tradeRecords[0])
	if trade["ticker"] != "AAPL" || trade["transaction_shares"] != 1439.0 || trade["transaction_price_per_share"] != 317.01 {
		t.Fatalf("unexpected insider trade: %#v", trade)
	}

	filters := []any{map[string]any{"field": "exchange", "operator": "eq", "value": "NASDAQ"}}
	screen, err := svc.Call(context.Background(), "key", "screen_stocks", map[string]any{"filters": filters, "limit": 10.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	screenRecords := asRecords(t, screen.Value)
	if len(screenRecords) != 1 {
		t.Fatalf("expected 1 screener result, got %d", len(screenRecords))
	}
	row := jsonRoundTrip(t, screenRecords[0])
	want := map[string]any{"ticker": "AAPL", "exchange": "NASDAQ", "market_cap": "4789955817800",
		"last_sale": "328.21", "net_change": "-3.25", "percent_change": "-0.01"}
	for key, expected := range want {
		if row[key] != expected {
			t.Fatalf("field %s: got %#v want %#v", key, row[key], expected)
		}
	}

	unsupported, err := svc.Call(context.Background(), "key", "screen_stocks",
		map[string]any{"filters": []any{map[string]any{"field": "revenue", "operator": "gt", "value": 1_000_000_000.0}}})
	var inputErr *providers.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected InputError for unsupported filter, got result=%#v err=%v", unsupported, err)
	}
}

func TestListStockScreenerFiltersIsStaticAndFree(t *testing.T) {
	svc, transport := newTestService(t, map[string]fakeOutcome{})
	result, err := svc.Call(context.Background(), "key", "list_stock_screener_filters", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	if _, ok := body["metrics"]; !ok {
		t.Fatalf("expected metrics key, got %#v", body)
	}
	if _, ok := body["operators"]; !ok {
		t.Fatalf("expected operators key, got %#v", body)
	}
	if transport.CallCount() != 0 {
		t.Fatalf("expected zero calls for a static catalog, got %d", transport.CallCount())
	}
}

func TestListFilingItemTypesIsStaticAndFree(t *testing.T) {
	svc, transport := newTestService(t, map[string]fakeOutcome{})
	result, err := svc.Call(context.Background(), "key", "list_filing_item_types", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	for _, key := range []string{"10-K", "10-Q", "8-K"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("expected %s key in catalog, got %#v", key, body)
		}
	}
	if transport.CallCount() != 0 {
		t.Fatalf("expected zero calls for a static catalog, got %d", transport.CallCount())
	}
}

func TestUnknownToolIsInputError(t *testing.T) {
	svc, transport := newTestService(t, map[string]fakeOutcome{})
	_, err := svc.Call(context.Background(), "key", "get_totally_made_up_tool", map[string]any{})
	var inputErr *providers.InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected InputError, got %T: %v", err, err)
	}
	if transport.CallCount() != 0 {
		t.Fatalf("expected zero calls, got %d", transport.CallCount())
	}
}

func TestFDShape_SegmentedFinancialsAndKPI(t *testing.T) {
	segFilings := []any{
		map[string]any{
			"filingDate": "2026-02-01", "reportDate": "2025-12-31", "form": "10-K",
			"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20251231.htm",
		},
	}
	extractPayload := map[string]any{
		"status": "ok", "url": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20251231.htm",
		"urls_analyzed": 1.0,
		"data": map[string]any{
			"product_net_sales": []any{
				map[string]any{
					"name": "iPhone", "metric": "net sales", "unit": "USD",
					"values": []any{
						map[string]any{"fiscal_year": 2025.0, "period_end": "2025-12-31", "value": 200_000_000.0},
					},
					"evidence_quote": "iPhone net sales", "evidence_section": "Segment information",
				},
			},
		},
	}
	kpiExtractPayload := map[string]any{
		"status": "ok", "url": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20251231.htm",
		"urls_analyzed": 1.0,
		"data": map[string]any{
			"kpis": []any{
				map[string]any{
					"name": "load_factor", "unit": "%", "period": "FY 2025", "value_text": "85%",
					"value": 85.0, "basis": "annual", "evidence_quote": "load factor of 85%",
				},
			},
		},
	}
	outcomes := map[string]fakeOutcome{
		"defillama /equities/v1/filings": {output: segFilings},
		"context.dev /web/extract":       {output: extractPayload},
	}
	svc, _ := newTestService(t, outcomes)
	result, err := svc.Call(context.Background(), "key", "get_segmented_financials", map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 segment period, got %d", len(records))
	}
	seg := jsonRoundTrip(t, records[0])
	if seg["ticker"] != "AAPL" || seg["report_period"] != "2025-12-31" || seg["fiscal_period"] != "FY2025" {
		t.Fatalf("unexpected segmented financials record: %#v", seg)
	}

	kpiOutcomes := map[string]fakeOutcome{
		"defillama /equities/v1/filings": {output: segFilings},
		"context.dev /web/extract":       {output: kpiExtractPayload},
	}
	kpiSvc, kpiTransport := newTestService(t, kpiOutcomes)
	kpiResult, err := kpiSvc.Call(context.Background(), "key", "get_kpi_metrics", map[string]any{"ticker": "AAPL", "period": "annual"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	kpiRecords := asRecords(t, kpiResult.Value)
	if len(kpiRecords) != 1 {
		t.Fatalf("expected 1 kpi metric, got %d", len(kpiRecords))
	}
	kpi := jsonRoundTrip(t, kpiRecords[0])
	if kpi["ticker"] != "AAPL" || kpi["metric_name"] != "load_factor" || kpi["value"] != 85.0 {
		t.Fatalf("unexpected kpi record: %#v", kpi)
	}
	if kpiTransport.CallCount() != 2 {
		t.Fatalf("expected filings + extract call, got %d", kpiTransport.CallCount())
	}
}

func TestFDShape_InstitutionalHoldings(t *testing.T) {
	payload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"name_of_issuer": "Apple Inc.",
			"rows": []any{
				map[string]any{
					"filer_name": "Vanguard Group Inc", "shares": 100_000.0, "value_usd": 20_000_000.0,
					"report_period": "2025-12-31",
				},
			},
		},
	}
	outcomes := map[string]fakeOutcome{"secform4 /get_institution_holders": {output: payload}}
	svc, _ := newTestService(t, outcomes)
	result, err := svc.Call(context.Background(), "key", "get_institutional_holdings", map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 holding, got %d", len(records))
	}
	holding := jsonRoundTrip(t, records[0])
	if holding["ticker"] != "AAPL" || holding["filer_name"] != "Vanguard Group Inc" || holding["shares"] != 100000.0 {
		t.Fatalf("unexpected holding: %#v", holding)
	}

	rejected, err := svc.Call(context.Background(), "key", "get_institutional_holdings", map[string]any{"filer_cik": "0001"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, rejected.Value)
	if body["error"] != "bad_request" {
		t.Fatalf("expected embedded bad_request response, got %#v", body)
	}
}

func TestInterestRates_ParsesFedRange(t *testing.T) {
	markdown := "The Federal Reserve set the federal funds rate 4.25 to 4.50 percent effective January 1, 2026."
	rate := parsePolicyRate(markdown, "FED")
	if rate == nil {
		t.Fatalf("expected a parsed FED rate")
	}
	if rate.Rate != 4.375 {
		t.Fatalf("expected midpoint rate 4.375, got %v", rate.Rate)
	}
	if rate.Date == nil || *rate.Date != "2026-01-01" {
		t.Fatalf("expected date 2026-01-01, got %v", rate.Date)
	}
}

func TestIndexFund_ParsesHoldingsTable(t *testing.T) {
	markdown := "As of December 31, 2025\n\n" +
		"| Ticker | Name | Weight |\n" +
		"|---|---|---|\n" +
		"| AAPL | Apple Inc | 7.5% |\n" +
		"| MSFT | Microsoft Corp | 6.2% |\n"
	holdings := parseFundHoldings(markdown)
	if len(holdings) != 2 {
		t.Fatalf("expected 2 holdings, got %d", len(holdings))
	}
	if holdings[0].Ticker == nil || *holdings[0].Ticker != "AAPL" {
		t.Fatalf("expected AAPL first (higher weight), got %#v", holdings[0])
	}
	asOf := parseFundAsOf(markdown)
	if asOf == nil || *asOf != "2025-12-31" {
		t.Fatalf("expected as_of 2025-12-31, got %v", asOf)
	}
}

func TestGetFilingItems_HappyPath(t *testing.T) {
	filings := []any{
		map[string]any{
			"filingDate": "2026-02-01", "reportDate": "2025-12-31", "form": "10-K",
			"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20251231.htm",
		},
	}
	scrapePayload := map[string]any{
		"success": true, "markdown": "# Item 1. Business\n\nWe design products.\n\n# Item 1A. Risk Factors\n\nRisks exist.\n",
		"url":           "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20251231.htm",
		"contentLength": 80.0,
	}
	outcomes := map[string]fakeOutcome{
		"defillama /equities/v1/filings":   {output: filings},
		"context.dev /web/scrape/markdown": {output: scrapePayload},
	}
	svc, _ := newTestService(t, outcomes)
	result, err := svc.Call(context.Background(), "key", "get_filing_items",
		map[string]any{"ticker": "AAPL", "filing_type": "10-K"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	if body["ticker"] != "AAPL" || body["filing_type"] != "10-K" {
		t.Fatalf("unexpected filing items response: %#v", body)
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("expected non-empty items, got %#v", body)
	}
}
