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
)

// ---- shared test fixtures ----

// fakeCall records one Caller.Call invocation for assertions.
type fakeCall struct {
	apiKey string
	tool   string
	args   map[string]any
}

// fakeCaller is a network-free Caller stand-in: it never touches Monid, and
// returns whatever the test configured per tool name.
type fakeCaller struct {
	mu      sync.Mutex
	calls   []fakeCall
	results map[string]Result
	errs    map[string]error
}

func newFakeCaller() *fakeCaller {
	return &fakeCaller{results: map[string]Result{}, errs: map[string]error{}}
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

func (f *fakeCaller) lastCall() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}
	}
	return f.calls[len(f.calls)-1]
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
	rec := doGet(t, rt, "/kpi/metrics", nil)
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
