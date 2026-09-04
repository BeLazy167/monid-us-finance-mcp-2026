package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/belazy/monid-finance/providers"
)

// ---- shared test fixtures ----

// fakeCall records one Caller.Call/Capability invocation for assertions.
type fakeCall struct {
	apiKey string
	tool   string
	args   map[string]any
}

// fakeCaller is a network-free Caller stand-in: it never touches Monid, and
// returns whatever the test configured per tool/capability name. Call and
// Capability are tracked on separate logs/result tables so a test can
// assert which surface a route actually used (e.g. a coverage-list route
// must only ever reach Capability, never Call).
type fakeCaller struct {
	mu               sync.Mutex
	calls            []fakeCall
	results          map[string]Result
	errs             map[string]error
	capabilityCalls  []fakeCall
	capabilityResult map[string]Result
	capabilityErrs   map[string]error
}

func newFakeCaller() *fakeCaller {
	return &fakeCaller{
		results:          map[string]Result{},
		errs:             map[string]error{},
		capabilityResult: map[string]Result{},
		capabilityErrs:   map[string]error{},
	}
}

func (f *fakeCaller) Call(ctx context.Context, apiKey, tool string, args map[string]any) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{apiKey: apiKey, tool: tool, args: args})
	f.mu.Unlock()
	if err, ok := f.errs[tool]; ok {
		return Result{}, err
	}
	if res, ok := f.results[tool]; ok {
		return res, nil
	}
	return Result{}, fmt.Errorf("fakeCaller: no result configured for tool %q", tool)
}

func (f *fakeCaller) Capability(ctx context.Context, apiKey, name string, args map[string]any) (Result, error) {
	f.mu.Lock()
	f.capabilityCalls = append(f.capabilityCalls, fakeCall{apiKey: apiKey, tool: name, args: args})
	f.mu.Unlock()
	if err, ok := f.capabilityErrs[name]; ok {
		return Result{}, err
	}
	if res, ok := f.capabilityResult[name]; ok {
		return res, nil
	}
	return Result{}, fmt.Errorf("fakeCaller: no result configured for capability %q", name)
}

func (f *fakeCaller) lastCall() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeCaller) lastCapabilityCall() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.capabilityCalls) == 0 {
		return fakeCall{}
	}
	return f.capabilityCalls[len(f.capabilityCalls)-1]
}

const testAPIKey = "test-monid-key"

func newTestRouter(caller Caller, mutate func(*Config)) *Router {
	cfg := Config{
		Caller:             caller,
		MCPHandler:         http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		StaticDir:          "testdata/site",
		RateLimitPerMinute: 100000, // effectively unbounded unless a test overrides it
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewRouter(cfg)
}

func doGet(t *testing.T, rt *Router, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec
}

func doPost(t *testing.T, rt *Router, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

type statementRecord struct {
	Ticker       string `json:"ticker"`
	ReportPeriod string `json:"report_period"`
}

func twelvePeriodStatements() []statementRecord {
	records := make([]statementRecord, 12)
	for i := range records {
		records[i] = statementRecord{Ticker: "AAPL", ReportPeriod: fmt.Sprintf("%d-12-31", 2025-i)}
	}
	return records
}

// ---- route shapes and wrapped keys ----

func TestIncomeStatements_WrappedShapeAndPagination(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_income_statement"] = Result{
		Value: twelvePeriodStatements(), WrapperKey: "income_statements", Paginate: true,
	}
	rt := newTestRouter(caller, nil)

	rec := doGet(t, rt, "/financials/income-statements?ticker=AAPL&period=annual&limit=12",
		map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	records, ok := body["income_statements"].([]any)
	if !ok {
		t.Fatalf("income_statements not a list: %#v", body["income_statements"])
	}
	if len(records) != 10 {
		t.Fatalf("got %d records, want 10 (one page)", len(records))
	}
	nextURL, ok := body["next_page_url"].(string)
	if !ok || nextURL == "" {
		t.Fatalf("next_page_url missing: %#v", body["next_page_url"])
	}
	if got := "http://example.com"; nextURL[:len(got)] != got {
		t.Fatalf("next_page_url = %q, want absolute URL on the inbound host", nextURL)
	}
	if !bytesContains(nextURL, "cursor=") {
		t.Fatalf("next_page_url = %q, want a cursor query param", nextURL)
	}

	// The call args reaching the fake caller carry no cursor: pagination is
	// entirely a REST-layer concern.
	if _, has := caller.lastCall().args["cursor"]; has {
		t.Fatalf("cursor leaked into Caller.Call args: %#v", caller.lastCall().args)
	}

	// Cursor round-trip: following next_page_url returns the remaining 2
	// records and no further next_page_url.
	req2 := httptest.NewRequest(http.MethodGet, nextURL, nil)
	req2.Header.Set(apiKeyHeader, testAPIKey)
	rec2 := httptest.NewRecorder()
	rt.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	body2 := decodeBody(t, rec2)
	records2 := body2["income_statements"].([]any)
	if len(records2) != 2 {
		t.Fatalf("page 2 got %d records, want 2", len(records2))
	}
	if _, has := body2["next_page_url"]; has {
		t.Fatalf("page 2 unexpectedly has next_page_url: %#v", body2["next_page_url"])
	}
}

func bytesContains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

func TestStatementRoutes_AsReportedRejected(t *testing.T) {
	for _, path := range []string{
		"/financials/income-statements",
		"/financials/balance-sheets",
		"/financials/cash-flow-statements",
	} {
		t.Run(path, func(t *testing.T) {
			caller := newFakeCaller()
			rt := newTestRouter(caller, nil)
			rec := doGet(t, rt, path+"?ticker=AAPL&as_reported=true", map[string]string{apiKeyHeader: testAPIKey})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			if body["error"] != "bad_request" {
				t.Fatalf("error = %v, want bad_request", body["error"])
			}
			if len(caller.calls) != 0 {
				t.Fatalf("as_reported=true must not reach the caller (zero cost), got %d calls", len(caller.calls))
			}
		})
	}
}

func TestFinancialMetricsSnapshot_Unwrapped(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_financial_metrics_snapshot"] = Result{
		Value: map[string]any{"snapshot": map[string]any{"ticker": "AAPL", "market_cap": 1.0}},
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financial-metrics/snapshot?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["snapshot"]; !ok {
		t.Fatalf("body missing snapshot: %#v", body)
	}
}

func TestPrices_TickerAndIntervalMultiplierValidation(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)

	rec := doGet(t, rt, "/prices?ticker=AAPL&interval_multiplier=2", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("interval_multiplier=2 status = %d, want 400", rec.Code)
	}
	if decodeBody(t, rec)["error"] != "bad_request" {
		t.Fatalf("interval_multiplier=2 error = %v, want bad_request", decodeBody(t, rec)["error"])
	}

	rec = doGet(t, rt, "/prices", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ticker status = %d, want 400", rec.Code)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("validation failures must not reach the caller, got %d calls", len(caller.calls))
	}
}

func TestPrices_ValidRequestWrapsTicker(t *testing.T) {
	caller := newFakeCaller()
	type priceRecord struct {
		Time  string  `json:"time"`
		Close float64 `json:"close"`
	}
	caller.results["get_stock_prices"] = Result{
		Value:      []priceRecord{{Time: "2026-01-01", Close: 100}},
		WrapperKey: "prices",
		Paginate:   true,
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/prices?ticker=aapl&start_date=2025-12-30&end_date=2026-01-01",
		map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ticker"] != "AAPL" {
		t.Fatalf("ticker = %v, want AAPL (uppercased)", body["ticker"])
	}
	prices, ok := body["prices"].([]any)
	if !ok || len(prices) != 1 {
		t.Fatalf("prices = %#v, want one record", body["prices"])
	}
	if got := caller.lastCall().args["ticker"]; got != "AAPL" {
		t.Fatalf("caller saw ticker = %v, want AAPL", got)
	}
}

func TestPriceSnapshot_TickerRequired(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/prices/snapshot", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestScreener_PostWrapsSearchResults(t *testing.T) {
	caller := newFakeCaller()
	caller.results["screen_stocks"] = Result{
		Value:      []map[string]any{{"ticker": "AAPL"}},
		WrapperKey: "search_results",
		Paginate:   false,
	}
	rt := newTestRouter(caller, nil)
	rec := doPost(t, rt, "/financials/search/screener", map[string]any{
		"filters": []map[string]any{{"field": "exchange", "operator": "eq", "value": "NASDAQ"}},
		"limit":   10,
	}, map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	results, ok := body["search_results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("search_results = %#v", body["search_results"])
	}
	if _, has := body["next_page_url"]; has {
		t.Fatalf("screener must never paginate, got next_page_url")
	}
}

func TestScreenerFilters_Unwrapped(t *testing.T) {
	caller := newFakeCaller()
	caller.results["list_stock_screener_filters"] = Result{
		Value: map[string]any{"metrics": map[string]any{}, "operators": []string{"eq"}},
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials/search/screener/filters", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["metrics"].(map[string]any); !ok {
		t.Fatalf("metrics missing/not an object: %#v", body["metrics"])
	}
	if _, ok := body["operators"].([]any); !ok {
		t.Fatalf("operators missing/not a list: %#v", body["operators"])
	}
}

func TestCompanyFacts_Unwrapped(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{"ticker": "AAPL"}}}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["company_facts"]; !ok {
		t.Fatalf("company_facts missing: %#v", body)
	}
}

func TestFilings_WrapsListAndForwardsRepeatedFilingType(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_filings"] = Result{Value: []map[string]any{{"ticker": "AAPL"}}, WrapperKey: "filings", Paginate: true}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/filings?ticker=AAPL&filing_type=10-K&filing_type=10-Q", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["filings"].([]any); !ok {
		t.Fatalf("filings missing/not a list: %#v", body)
	}
	types, ok := caller.lastCall().args["filing_type"].([]any)
	if !ok || len(types) != 2 {
		t.Fatalf("filing_type args = %#v, want [10-K 10-Q]", caller.lastCall().args["filing_type"])
	}
}

func TestFilingItems_RequiredFieldsValidated(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/filings/items?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing filing_type/year)", rec.Code)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("missing required fields must not reach the caller")
	}

	caller.results["get_filing_items"] = Result{Value: map[string]any{"items": []any{}}}
	rec = doGet(t, rt, "/filings/items?ticker=AAPL&filing_type=10-K&year=2025",
		map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestNotImplementedRoutes_200WithErrorBody(t *testing.T) {
	for _, path := range notImplementedPaths {
		t.Run(path, func(t *testing.T) {
			caller := newFakeCaller()
			rt := newTestRouter(caller, nil)
			rec := doGet(t, rt, path+"?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			if body["error"] != "not_implemented" {
				t.Fatalf("error = %v, want not_implemented", body["error"])
			}
			if _, ok := body["message"].(string); !ok {
				t.Fatalf("message missing/not a string: %#v", body)
			}
			if len(caller.calls) != 0 {
				t.Fatalf("not-implemented routes must never reach the caller")
			}
		})
	}
	// Still requires auth: the stub is behind the same gate as real routes.
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/kpi/metrics/tickers", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a key", rec.Code)
	}
}

// ---- pagination helpers ----

func TestPaginateValue(t *testing.T) {
	records := []int{0, 1, 2, 3, 4}
	page, hasMore := paginateValue(records, 0, 3)
	if got := page.([]int); len(got) != 3 || got[0] != 0 || got[2] != 2 {
		t.Fatalf("page = %#v", got)
	}
	if !hasMore {
		t.Fatalf("hasMore = false, want true")
	}
	page, hasMore = paginateValue(records, 3, 3)
	if got := page.([]int); len(got) != 2 {
		t.Fatalf("page = %#v", got)
	}
	if hasMore {
		t.Fatalf("hasMore = true, want false")
	}
	page, hasMore = paginateValue(records, 10, 3)
	if got := page.([]int); len(got) != 0 {
		t.Fatalf("page = %#v, want empty", got)
	}
	if hasMore {
		t.Fatalf("hasMore = true, want false past the end")
	}
}

func TestCursorRoundTrip(t *testing.T) {
	cursor := encodeCursor(42)
	offset, err := cursorOffset(cursor)
	if err != nil {
		t.Fatalf("cursorOffset: %v", err)
	}
	if offset != 42 {
		t.Fatalf("offset = %d, want 42", offset)
	}
	if _, err := cursorOffset("not-a-valid-cursor!!!"); err == nil {
		t.Fatalf("expected an error decoding a malformed cursor")
	}
	if _, err := cursorOffset(""); err != nil {
		t.Fatalf("empty cursor should decode to offset 0, got error: %v", err)
	}
}

// ---- Segmented financials (get_segmented_financials) ----

func TestSegmentedFinancials_CombinedPassesRecordThrough(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_segmented_financials"] = Result{
		Value: []map[string]any{{
			"ticker":        "AAPL",
			"report_period": "2025-09-27",
			"income_statement": map[string]any{
				"revenue": map[string]any{
					"product": []map[string]any{{"label": "iPhone", "value": 200000000000.0}},
				},
			},
		}},
		WrapperKey: "segmented_financials",
		Paginate:   true,
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials/segments?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	records, ok := body["segmented_financials"].([]any)
	if !ok || len(records) != 1 {
		t.Fatalf("segmented_financials = %#v", body["segmented_financials"])
	}
	record := records[0].(map[string]any)
	if _, ok := record["income_statement"]; !ok {
		t.Fatalf("combined route must pass income_statement through unchanged: %#v", record)
	}
	if got := caller.lastCall().args["period"]; got != "annual" {
		t.Fatalf("period default = %v, want annual", got)
	}
	if got := caller.lastCall().args["limit"]; got != float64(4) {
		t.Fatalf("limit default = %v, want 4", got)
	}
}

func TestSegmentedFinancials_IncomeStatementHoistsRevenue(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_segmented_financials"] = Result{
		Value: []map[string]any{{
			"ticker":        "AAPL",
			"report_period": "2025-09-27",
			"fiscal_period": "FY2025",
			"period":        "annual",
			"income_statement": map[string]any{
				"revenue": map[string]any{
					"product": []map[string]any{{"label": "iPhone", "value": 200000000000.0}},
				},
			},
		}},
		WrapperKey: "segmented_financials",
		Paginate:   true,
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials/income-statements/segments?ticker=AAPL",
		map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	records := body["segmented_financials"].([]any)
	record := records[0].(map[string]any)
	if _, ok := record["income_statement"]; ok {
		t.Fatalf("income-statement route must hoist revenue, not nest it: %#v", record)
	}
	revenue, ok := record["revenue"].(map[string]any)
	if !ok {
		t.Fatalf("revenue missing at top level: %#v", record)
	}
	product, ok := revenue["product"].([]any)
	if !ok || len(product) != 1 {
		t.Fatalf("revenue.product = %#v", revenue["product"])
	}
	if _, has := record["operating_income"]; has {
		t.Fatalf("operating_income was never sourced, must be omitted, got %#v", record["operating_income"])
	}
}

func TestSegmentedFinancials_BalanceSheetAndCashFlowOmitUnsourcedFields(t *testing.T) {
	for _, tc := range []struct {
		path   string
		absent []string
	}{
		{"/financials/balance-sheets/segments", []string{"assets", "goodwill", "long_lived_assets", "income_statement", "revenue"}},
		{"/financials/cash-flow-statements/segments", []string{"capital_expenditure", "income_statement", "revenue"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			caller := newFakeCaller()
			caller.results["get_segmented_financials"] = Result{
				Value: []map[string]any{{
					"ticker":        "AAPL",
					"report_period": "2025-09-27",
					"income_statement": map[string]any{
						"revenue": map[string]any{
							"product": []map[string]any{{"label": "iPhone", "value": 200000000000.0}},
						},
					},
				}},
				WrapperKey: "segmented_financials",
				Paginate:   true,
			}
			rt := newTestRouter(caller, nil)
			rec := doGet(t, rt, tc.path+"?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			records := body["segmented_financials"].([]any)
			record := records[0].(map[string]any)
			if record["ticker"] != "AAPL" {
				t.Fatalf("ticker = %v, want AAPL (metadata must survive)", record["ticker"])
			}
			for _, key := range tc.absent {
				if _, has := record[key]; has {
					t.Fatalf("%s must never be fabricated on %s, got %#v", key, tc.path, record[key])
				}
			}
		})
	}
}

func TestSegmentedFinancials_NotFoundErrorPassesThroughUnreshaped(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_segmented_financials"] = Result{
		Value: map[string]any{"error": "not_found", "message": "No 10-K filing exists for ticker AAPL."},
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials/balance-sheets/segments?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["error"] != "not_found" {
		t.Fatalf("error = %v, want not_found", body["error"])
	}
}

func TestSegmentedFinancials_LimitValidatedBeforeCall(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials/segments?ticker=AAPL&limit=not-a-number", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("bad limit must not reach the caller, got %d calls", len(caller.calls))
	}
}

func TestSegmentedFinancials_QuarterlyRejectedByTool(t *testing.T) {
	caller := newFakeCaller()
	caller.errs["get_segmented_financials"] = &providers.InputError{Msg: "period must be annual: the validated route extracts the annual 10-K"}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials/segments?ticker=AAPL&period=quarterly", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if decodeBody(t, rec)["error"] != "bad_request" {
		t.Fatalf("error = %v, want bad_request", decodeBody(t, rec)["error"])
	}
}

// ---- KPI extraction (get_kpi_metrics / get_kpi_guidance / get_kpi_non_gaap) ----

func TestKPIRoutes_WrappedShapeAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		path       string
		tool       string
		wrapperKey string
	}{
		{"/kpi/metrics", "get_kpi_metrics", "kpi_metrics"},
		{"/kpi/guidance", "get_kpi_guidance", "kpi_guidance"},
		{"/kpi/non-gaap", "get_kpi_non_gaap", "kpi_non_gaap"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			caller := newFakeCaller()
			caller.results[tc.tool] = Result{
				Value:      []map[string]any{{"ticker": "AAPL", "metric_name": "iPhone units sold"}},
				WrapperKey: tc.wrapperKey,
				Paginate:   true,
			}
			rt := newTestRouter(caller, nil)
			rec := doGet(t, rt, tc.path+"?ticker=AAPL&metric_name=iPhone+units+sold&report_period_gte=2025-01-01&report_period_lte=2025-12-31",
				map[string]string{apiKeyHeader: testAPIKey})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			if _, ok := body[tc.wrapperKey].([]any); !ok {
				t.Fatalf("%s missing/not a list: %#v", tc.wrapperKey, body)
			}
			args := caller.lastCall().args
			if args["period"] != "quarterly" {
				t.Fatalf("period default = %v, want quarterly", args["period"])
			}
			if args["limit"] != float64(4) {
				t.Fatalf("limit default = %v, want 4", args["limit"])
			}
			if args["ticker"] != "AAPL" {
				t.Fatalf("ticker = %v, want AAPL", args["ticker"])
			}
			if args["metric_name"] != "iPhone units sold" {
				t.Fatalf("metric_name = %v, want %q", args["metric_name"], "iPhone units sold")
			}
			if args["report_period_gte"] != "2025-01-01" || args["report_period_lte"] != "2025-12-31" {
				t.Fatalf("report_period args = %#v", args)
			}
		})
	}
}

func TestKPIRoutes_TickerRequiredMapsToBadRequest(t *testing.T) {
	caller := newFakeCaller()
	caller.errs["get_kpi_metrics"] = &providers.InputError{Msg: "ticker is required"}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/kpi/metrics", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if decodeBody(t, rec)["error"] != "bad_request" {
		t.Fatalf("error = %v, want bad_request", decodeBody(t, rec)["error"])
	}
}

func TestKPIRoutes_LimitValidatedBeforeCall(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/kpi/guidance?ticker=AAPL&limit=abc", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("bad limit must not reach the caller, got %d calls", len(caller.calls))
	}
}

// ---- Central bank interest rates (get_interest_rates) ----

func TestInterestRates_BothRoutesCallSameZeroArgTool(t *testing.T) {
	for _, path := range []string{"/macro/interest-rates", "/macro/interest-rates/snapshot"} {
		t.Run(path, func(t *testing.T) {
			caller := newFakeCaller()
			caller.results["get_interest_rates"] = Result{
				Value:      []map[string]any{{"bank": "FED", "name": "Federal Reserve", "rate": 4.25}},
				WrapperKey: "interest_rates",
			}
			rt := newTestRouter(caller, nil)
			rec := doGet(t, rt, path+"?bank=FED", map[string]string{apiKeyHeader: testAPIKey})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			rates, ok := body["interest_rates"].([]any)
			if !ok || len(rates) != 1 {
				t.Fatalf("interest_rates = %#v", body["interest_rates"])
			}
			if len(caller.lastCall().args) != 0 {
				t.Fatalf("get_interest_rates takes no parameters, got args %#v", caller.lastCall().args)
			}
			if _, has := body["next_page_url"]; has {
				t.Fatalf("interest rates must never paginate, got next_page_url")
			}
		})
	}
}

func TestInterestRateBanks_DerivedFromServiceCapabilityNoToolCall(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["list_interest_rate_banks"] = Result{
		Value: map[string]any{
			"resource": "interest_rate_banks",
			"banks": []any{
				map[string]any{"bank": "FED", "name": "Federal Reserve"},
				map[string]any{"bank": "ECB", "name": "European Central Bank"},
				map[string]any{"bank": "BOE", "name": "Bank of England"},
				map[string]any{"bank": "BOJ", "name": "Bank of Japan"},
			},
		},
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/macro/interest-rates/banks", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	banks, ok := body["banks"].([]any)
	if !ok {
		t.Fatalf("banks missing/not a list: %#v", body)
	}
	want := map[string]bool{"FED": true, "ECB": true, "BOE": true, "BOJ": true}
	if len(banks) != len(want) {
		t.Fatalf("banks = %#v, want exactly %v", banks, want)
	}
	for _, b := range banks {
		row, ok := b.(map[string]any)
		if !ok {
			t.Fatalf("bank row is not an object: %#v", b)
		}
		code, _ := row["bank"].(string)
		if !want[code] {
			t.Fatalf("unexpected bank code %v", row["bank"])
		}
		if _, ok := row["name"].(string); !ok {
			t.Fatalf("bank row missing name: %#v", row)
		}
	}
	if body["resource"] != "interest_rate_banks" {
		t.Fatalf("resource = %#v, want interest_rate_banks", body["resource"])
	}
	// This route derives its answer from Service.ListInterestRateBanks
	// (bankSpecs) rather than a hand-typed copy: it must reach the
	// Capability surface exactly once and the tool/Call surface never.
	if len(caller.calls) != 0 {
		t.Fatalf("the banks coverage list must never reach Caller.Call, got %d calls", len(caller.calls))
	}
	if len(caller.capabilityCalls) != 1 {
		t.Fatalf("the banks coverage list must reach Caller.Capability exactly once, got %d calls", len(caller.capabilityCalls))
	}
	if caller.lastCapabilityCall().tool != "list_interest_rate_banks" {
		t.Fatalf("capability name = %q, want list_interest_rate_banks", caller.lastCapabilityCall().tool)
	}
}

func TestInterestRateBanks_StillRequiresAuth(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/macro/interest-rates/banks", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without a key", rec.Code)
	}
}

// ---- Index fund holdings (get_index_fund) ----

func TestIndexFunds_UnwrappedShapeAndDefaults(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_index_fund"] = Result{
		Value: map[string]any{
			"ticker": "SPY",
			"fund":   map[string]any{"name": "SPDR S&P 500 ETF Trust", "total_holdings": 503, "returned": 1, "offset": 0},
			"holdings": []map[string]any{
				{"ticker": "AAPL", "weight": 7.1},
			},
		},
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/index-funds?ticker=SPY", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ticker"] != "SPY" {
		t.Fatalf("ticker = %v, want SPY", body["ticker"])
	}
	if _, ok := body["fund"].(map[string]any); !ok {
		t.Fatalf("fund missing/not an object: %#v", body)
	}
	if _, ok := body["holdings"].([]any); !ok {
		t.Fatalf("holdings missing/not a list: %#v", body)
	}
	args := caller.lastCall().args
	if args["limit"] != float64(50) {
		t.Fatalf("limit default = %v, want 50", args["limit"])
	}
	if args["offset"] != float64(0) {
		t.Fatalf("offset default = %v, want 0", args["offset"])
	}
}

func TestIndexFunds_TickerRequiredMapsToBadRequest(t *testing.T) {
	caller := newFakeCaller()
	caller.errs["get_index_fund"] = &providers.InputError{Msg: "ticker is required"}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/index-funds", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if decodeBody(t, rec)["error"] != "bad_request" {
		t.Fatalf("error = %v, want bad_request", decodeBody(t, rec)["error"])
	}
}

func TestIndexFunds_LimitAndOffsetValidatedBeforeCall(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	for _, q := range []string{"limit=nope", "offset=nope"} {
		rec := doGet(t, rt, "/index-funds?ticker=SPY&"+q, map[string]string{apiKeyHeader: testAPIKey})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", q, rec.Code)
		}
	}
	if len(caller.calls) != 0 {
		t.Fatalf("bad limit/offset must not reach the caller, got %d calls", len(caller.calls))
	}
}

// ---- 13F institutional holdings (get_institutional_holdings) ----

func TestInstitutionalHoldings_WrappedShapeAndDefaults(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_institutional_holdings"] = Result{
		Value:      []map[string]any{{"ticker": "AAPL", "filer_name": "Vanguard"}},
		WrapperKey: "institutional_holdings",
		Paginate:   true,
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/institutional-holdings?ticker=AAPL&report_period_gte=2025-01-01",
		map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["institutional_holdings"].([]any); !ok {
		t.Fatalf("institutional_holdings missing/not a list: %#v", body)
	}
	args := caller.lastCall().args
	if args["limit"] != float64(10) {
		t.Fatalf("limit default = %v, want 10", args["limit"])
	}
	if args["ticker"] != "AAPL" {
		t.Fatalf("ticker = %v, want AAPL", args["ticker"])
	}
	if args["report_period_gte"] != "2025-01-01" {
		t.Fatalf("report_period_gte = %v, want 2025-01-01", args["report_period_gte"])
	}
}

func TestInstitutionalHoldings_FilerCIKRejectedMapsToBadRequest(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_institutional_holdings"] = Result{
		Value: map[string]any{"error": "bad_request", "message": "filer_cik lookup is not routed; pass ticker instead"},
	}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/institutional-holdings?filer_cik=0000102909", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (this is the tool's own honest bad_request body, not an HTTP error): %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["error"] != "bad_request" {
		t.Fatalf("error = %v, want bad_request", body["error"])
	}
	if args := caller.lastCall().args["filer_cik"]; args != "0000102909" {
		t.Fatalf("filer_cik = %v, want passed through to the tool", args)
	}
}

func TestInstitutionalHoldings_LimitValidatedBeforeCall(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/institutional-holdings?ticker=AAPL&limit=nope", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("bad limit must not reach the caller, got %d calls", len(caller.calls))
	}
}

// ---- Coverage lists: tickers each route ACCEPTS (list*Tickers capabilities) ----

// coverageRouteCases pairs every list*Tickers REST route with the
// Capability name it must reach - all eight, including the two that used
// to stub as not_implemented (/institutional-holdings/tickers,
// /kpi/metrics/tickers) before this port's brief reframed them as
// accept-universe lists.
var coverageRouteCases = []struct {
	path       string
	capability string
}{
	{"/company/facts/tickers", "list_company_facts_tickers"},
	{"/earnings/tickers", "list_earnings_tickers"},
	{"/filings/tickers", "list_filings_tickers"},
	{"/financial-metrics/snapshot/tickers", "list_metrics_snapshot_tickers"},
	{"/prices/tickers", "list_prices_tickers"},
	{"/prices/snapshot/tickers", "list_price_snapshot_tickers"},
	{"/institutional-holdings/tickers", "list_institutional_holdings_tickers"},
	{"/kpi/metrics/tickers", "list_kpi_tickers"},
}

// coverageFakeTickers is a small, deliberately-not-3227-sized fixture: big
// enough (5) to exercise multi-page cursor pagination with a REST `limit`
// of 2, small enough to hand-check every page.
var coverageFakeTickers = []any{"AAPL", "GOOG", "MSFT", "NVDA", "TSLA"}

func coverageFakeResult(resource string) Result {
	return Result{Value: map[string]any{
		"resource": resource,
		"total":    float64(len(coverageFakeTickers)),
		"tickers":  coverageFakeTickers,
	}}
}

func TestCoverageTickers_ShapeAndCapabilitySurface(t *testing.T) {
	for _, tc := range coverageRouteCases {
		t.Run(tc.path, func(t *testing.T) {
			caller := newFakeCaller()
			caller.capabilityResult[tc.capability] = coverageFakeResult("some_resource")
			rt := newTestRouter(caller, nil)
			rec := doGet(t, rt, tc.path, map[string]string{apiKeyHeader: testAPIKey})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			if body["resource"] != "some_resource" {
				t.Fatalf("resource = %#v, want some_resource", body["resource"])
			}
			if body["total"] != float64(5) {
				t.Fatalf("total = %#v, want 5", body["total"])
			}
			tickers, ok := body["tickers"].([]any)
			if !ok || len(tickers) != 5 {
				t.Fatalf("tickers = %#v, want all 5 (default limit 1000 > fixture size)", body["tickers"])
			}
			if _, has := body["next_page_url"]; has {
				t.Fatalf("unexpected next_page_url with default limit: %#v", body["next_page_url"])
			}
			// This route is a coverage-list capability, never a billable
			// MCP tool call.
			if len(caller.calls) != 0 {
				t.Fatalf("coverage list must never reach Caller.Call, got %d calls", len(caller.calls))
			}
			if len(caller.capabilityCalls) != 1 {
				t.Fatalf("coverage list must reach Caller.Capability exactly once, got %d calls", len(caller.capabilityCalls))
			}
			if got := caller.lastCapabilityCall().tool; got != tc.capability {
				t.Fatalf("capability name = %q, want %q", got, tc.capability)
			}
			// Every page always asks the capability for the full universe
			// (coverageMaxLimit), regardless of the caller's own REST
			// `limit`, so "total" never depends on pagination.
			if got := caller.lastCapabilityCall().args["limit"]; got != float64(coverageMaxLimit) {
				t.Fatalf("capability limit arg = %#v, want %d", got, coverageMaxLimit)
			}
		})
	}
}

func TestCoverageTickers_LimitCursorPagination(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["list_company_facts_tickers"] = coverageFakeResult("company_facts")
	rt := newTestRouter(caller, nil)

	rec := doGet(t, rt, "/company/facts/tickers?limit=2", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1 status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	page1, _ := body["tickers"].([]any)
	if len(page1) != 2 || page1[0] != "AAPL" || page1[1] != "GOOG" {
		t.Fatalf("page 1 tickers = %#v, want [AAPL GOOG]", body["tickers"])
	}
	if body["total"] != float64(5) {
		t.Fatalf("page 1 total = %#v, want 5 (full universe, independent of limit)", body["total"])
	}
	nextURL, ok := body["next_page_url"].(string)
	if !ok || nextURL == "" {
		t.Fatalf("page 1 missing next_page_url: %#v", body["next_page_url"])
	}

	req2 := httptest.NewRequest(http.MethodGet, nextURL, nil)
	req2.Header.Set(apiKeyHeader, testAPIKey)
	rec2 := httptest.NewRecorder()
	rt.ServeHTTP(rec2, req2)
	body2 := decodeBody(t, rec2)
	page2, _ := body2["tickers"].([]any)
	if len(page2) != 2 || page2[0] != "MSFT" || page2[1] != "NVDA" {
		t.Fatalf("page 2 tickers = %#v, want [MSFT NVDA]", body2["tickers"])
	}
	if body2["total"] != float64(5) {
		t.Fatalf("page 2 total = %#v, want 5", body2["total"])
	}
	nextURL2, ok := body2["next_page_url"].(string)
	if !ok || nextURL2 == "" {
		t.Fatalf("page 2 missing next_page_url: %#v", body2["next_page_url"])
	}

	req3 := httptest.NewRequest(http.MethodGet, nextURL2, nil)
	req3.Header.Set(apiKeyHeader, testAPIKey)
	rec3 := httptest.NewRecorder()
	rt.ServeHTTP(rec3, req3)
	body3 := decodeBody(t, rec3)
	page3, _ := body3["tickers"].([]any)
	if len(page3) != 1 || page3[0] != "TSLA" {
		t.Fatalf("page 3 tickers = %#v, want [TSLA]", body3["tickers"])
	}
	if _, has := body3["next_page_url"]; has {
		t.Fatalf("page 3 unexpectedly has next_page_url: %#v", body3["next_page_url"])
	}

	// Every page shares one capability call: the underlying capability
	// only ever fetches the full universe once per REST request, and
	// each page re-requests it with the identical args (limit=max),
	// which the shared TTL cache upstream would collapse to one Monid
	// fetch across all three pages.
	if len(caller.capabilityCalls) != 3 {
		t.Fatalf("expected 3 capability calls (one per REST page request), got %d", len(caller.capabilityCalls))
	}
}

func TestCoverageTickers_LimitValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"zero", "limit=0"},
		{"negative", "limit=-1"},
		{"above_max", "limit=5001"},
		{"not_an_integer", "limit=abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := newFakeCaller()
			rt := newTestRouter(caller, nil)
			rec := doGet(t, rt, "/company/facts/tickers?"+tc.query, map[string]string{apiKeyHeader: testAPIKey})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if body := decodeBody(t, rec); body["error"] != "bad_request" {
				t.Fatalf("error = %v, want bad_request", body["error"])
			}
			if len(caller.capabilityCalls) != 0 {
				t.Fatalf("invalid limit must not reach the caller, got %d calls", len(caller.capabilityCalls))
			}
		})
	}
	t.Run("max_is_allowed", func(t *testing.T) {
		caller := newFakeCaller()
		caller.capabilityResult["list_company_facts_tickers"] = coverageFakeResult("company_facts")
		rt := newTestRouter(caller, nil)
		rec := doGet(t, rt, "/company/facts/tickers?limit=5000", map[string]string{apiKeyHeader: testAPIKey})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestCoverageTickers_InvalidCursorRejected(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/company/facts/tickers?cursor=not-valid-base64url!!", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["error"] != "invalid_cursor" {
		t.Fatalf("error = %v, want invalid_cursor", body["error"])
	}
	if len(caller.capabilityCalls) != 0 {
		t.Fatalf("invalid cursor must not reach the caller, got %d calls", len(caller.capabilityCalls))
	}
}

func TestCoverageTickers_CapabilityErrorPropagates(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityErrs["list_company_facts_tickers"] = &providers.SchemaDriftError{Msg: "provider payload is not valid JSON"}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/company/facts/tickers", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
}

// ---- Static catalogs, zero paid calls (list_filing_types / list_filing_item_types) ----

func TestFilingTypes_ZeroCallStaticCatalog(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["list_filing_types"] = Result{Value: map[string]any{
		"resource":     "filing_types",
		"filing_types": []any{"10-K", "10-Q", "8-K", "20-F", "6-K"},
	}}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/filings/types", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["resource"] != "filing_types" {
		t.Fatalf("resource = %#v, want filing_types", body["resource"])
	}
	types, ok := body["filing_types"].([]any)
	if !ok || len(types) != 5 {
		t.Fatalf("filing_types = %#v, want 5 entries", body["filing_types"])
	}
	if len(caller.calls) != 0 {
		t.Fatalf("/filings/types must issue zero Caller.Call (tool) invocations, got %d", len(caller.calls))
	}
	if len(caller.capabilityCalls) != 1 {
		t.Fatalf("/filings/types must reach Caller.Capability exactly once, got %d", len(caller.capabilityCalls))
	}
	if caller.lastCapabilityCall().tool != "list_filing_types" {
		t.Fatalf("capability name = %q, want list_filing_types", caller.lastCapabilityCall().tool)
	}
}

func TestFilingItemTypes_ZeroCallStaticCatalogAndOptionalFilter(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["list_filing_item_types"] = Result{Value: map[string]any{
		"10-K": []any{map[string]any{"name": "item_1", "title": "Business", "description": "Business"}},
	}}
	rt := newTestRouter(caller, nil)

	rec := doGet(t, rt, "/filings/items/types?filing_type=10-K", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["10-K"]; !ok {
		t.Fatalf("body missing 10-K: %#v", body)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("/filings/items/types must issue zero Caller.Call (tool) invocations, got %d", len(caller.calls))
	}
	if len(caller.capabilityCalls) != 1 {
		t.Fatalf("/filings/items/types must reach Caller.Capability exactly once, got %d", len(caller.capabilityCalls))
	}
	if got := caller.lastCapabilityCall().args["filing_type"]; got != "10-K" {
		t.Fatalf("filing_type arg = %#v, want 10-K", got)
	}

	// filing_type is optional: omitting it must not add the key at all
	// (mirroring putQueryString's own present-only-if-non-empty contract).
	rec2 := doGet(t, rt, "/filings/items/types", map[string]string{apiKeyHeader: testAPIKey})
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	if _, has := caller.lastCapabilityCall().args["filing_type"]; has {
		t.Fatalf("filing_type must be absent when omitted from the query string: %#v", caller.lastCapabilityCall().args)
	}
}

// ---- All financials (get_all_financials) ----

func TestAllFinancials_AsReportedRejectedZeroCost(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials?ticker=AAPL&as_reported=true", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["error"] != "bad_request" {
		t.Fatalf("error = %v, want bad_request", body["error"])
	}
	if len(caller.calls) != 0 || len(caller.capabilityCalls) != 0 {
		t.Fatalf("as_reported=true must not reach the caller, got %d Call + %d Capability", len(caller.calls), len(caller.capabilityCalls))
	}
}

func TestAllFinancials_UnwrappedShapeAndDefaults(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["get_all_financials"] = Result{Value: map[string]any{"financials": map[string]any{
		"income_statements":    []any{map[string]any{"ticker": "AAPL"}},
		"balance_sheets":       []any{},
		"cash_flow_statements": []any{},
	}}}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/financials?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	financials, ok := body["financials"].(map[string]any)
	if !ok {
		t.Fatalf("financials missing/not an object: %#v", body)
	}
	if _, ok := financials["income_statements"]; !ok {
		t.Fatalf("financials.income_statements missing: %#v", financials)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("get_all_financials is a capability, not a tool: got %d Call invocations", len(caller.calls))
	}
	args := caller.lastCapabilityCall().args
	if args["period"] != "annual" {
		t.Fatalf("period default = %#v, want annual", args["period"])
	}
	if args["limit"] != float64(4) {
		t.Fatalf("limit default = %#v, want 4", args["limit"])
	}
	if args["ticker"] != "AAPL" {
		t.Fatalf("ticker = %#v, want AAPL", args["ticker"])
	}
}

func TestAllFinancials_ForwardsPeriodLimitAndReportPeriodFilters(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["get_all_financials"] = Result{Value: map[string]any{"financials": map[string]any{}}}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt,
		"/financials?ticker=AAPL&period=quarterly&limit=8&report_period_gte=2020-01-01&report_period_lte=2024-12-31",
		map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	args := caller.lastCapabilityCall().args
	if args["period"] != "quarterly" {
		t.Fatalf("period = %#v, want quarterly", args["period"])
	}
	if args["limit"] != float64(8) {
		t.Fatalf("limit = %#v, want 8", args["limit"])
	}
	if args["report_period_gte"] != "2020-01-01" || args["report_period_lte"] != "2024-12-31" {
		t.Fatalf("report_period filters not forwarded: %#v", args)
	}
}

// ---- Line-item search (search_line_items) ----

func TestSearchLineItems_PostWrapsSearchResultsAndForwardsBody(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["search_line_items"] = Result{
		Value:      []map[string]any{{"ticker": "AAPL", "report_period": "2024-12-31", "period": "ttm", "revenue": 1.0}},
		WrapperKey: "search_results",
	}
	rt := newTestRouter(caller, nil)
	rec := doPost(t, rt, "/financials/search/line-items", map[string]any{
		"line_items": []string{"revenue", "net_income"},
		"tickers":    []string{"AAPL", "MSFT"},
		"period":     "annual",
		"limit":      2,
	}, map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	results, ok := body["search_results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("search_results = %#v", body["search_results"])
	}
	if len(caller.calls) != 0 {
		t.Fatalf("search_line_items is a capability, not a tool: got %d Call invocations", len(caller.calls))
	}
	args := caller.lastCapabilityCall().args
	lineItems, ok := args["line_items"].([]any)
	if !ok || len(lineItems) != 2 || lineItems[0] != "revenue" || lineItems[1] != "net_income" {
		t.Fatalf("line_items = %#v, want [revenue net_income]", args["line_items"])
	}
	tickers, ok := args["tickers"].([]any)
	if !ok || len(tickers) != 2 || tickers[0] != "AAPL" || tickers[1] != "MSFT" {
		t.Fatalf("tickers = %#v, want [AAPL MSFT]", args["tickers"])
	}
	if args["period"] != "annual" || args["limit"] != float64(2) {
		t.Fatalf("period/limit = %#v/%#v, want annual/2", args["period"], args["limit"])
	}
}

func TestSearchLineItems_DefaultsPeriodAndLimitWhenOmitted(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityResult["search_line_items"] = Result{Value: []map[string]any{}, WrapperKey: "search_results"}
	rt := newTestRouter(caller, nil)
	rec := doPost(t, rt, "/financials/search/line-items", map[string]any{
		"line_items": []string{"revenue"},
		"tickers":    []string{"AAPL"},
	}, map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	args := caller.lastCapabilityCall().args
	if args["period"] != "ttm" {
		t.Fatalf("period default = %#v, want ttm", args["period"])
	}
	if args["limit"] != float64(1) {
		t.Fatalf("limit default = %#v, want 1", args["limit"])
	}
}

func TestSearchLineItems_MalformedBodyRejectedZeroCost(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	req := httptest.NewRequest(http.MethodPost, "/financials/search/line-items", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(apiKeyHeader, testAPIKey)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if len(caller.capabilityCalls) != 0 {
		t.Fatalf("malformed body must not reach the caller, got %d calls", len(caller.capabilityCalls))
	}
}

func TestSearchLineItems_ValidationErrorPropagates(t *testing.T) {
	caller := newFakeCaller()
	caller.capabilityErrs["search_line_items"] = &providers.InputError{Msg: "tickers must include at most 5 entries"}
	rt := newTestRouter(caller, nil)
	rec := doPost(t, rt, "/financials/search/line-items", map[string]any{
		"line_items": []string{"revenue"},
		"tickers":    []string{"A", "B", "C", "D", "E", "F"},
	}, map[string]string{apiKeyHeader: testAPIKey})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["error"] != "bad_request" {
		t.Fatalf("error = %v, want bad_request", body["error"])
	}
}
