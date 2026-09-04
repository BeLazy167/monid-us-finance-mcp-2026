package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// restAPI holds the dependencies REST handlers need.
type restAPI struct {
	caller Caller
}

// routeHandler answers one REST request for an already-authorized caller.
type routeHandler func(w http.ResponseWriter, r *http.Request, id callerIdentity)

// restRoute is one entry in the REST route table.
type restRoute struct {
	method  string
	path    string
	handler routeHandler
}

// notImplementedMessage matches the Python service's honest-failure text:
// the call is free and no data is fabricated for routes this server does
// not yet implement.
const notImplementedMessage = "This Financial Datasets route is not implemented by the Monid-backed " +
	"server yet; the call was free and no data was fabricated."

// notImplementedPaths are Financial Datasets REST routes this server does
// not implement yet, ported verbatim from rest_api.py's stub list. Each
// answers 200 {"error": "not_implemented", "message": ...} once
// authorized, at zero cost.
var notImplementedPaths = []string{
	"/kpi/metrics",
	"/kpi/metrics/tickers",
	"/kpi/metrics/sectors",
	"/kpi/guidance",
	"/kpi/non-gaap",
	"/macro/interest-rates",
	"/macro/interest-rates/snapshot",
	"/macro/interest-rates/banks",
	"/financials/segments",
	"/financials/income-statements/segments",
	"/financials/balance-sheets/segments",
	"/financials/cash-flow-statements/segments",
	"/index-funds",
	"/index-funds/tickers",
	"/institutional-holdings",
	"/institutional-holdings/investors",
	"/institutional-holdings/tickers",
	"/insider-ownership",
	"/beneficial-ownership",
	"/activist-ownership",
}

// restRoutes builds the full REST route table, ported route-for-route from
// src/monid_finance_mcp/rest_api.py.
func restRoutes(rt *restAPI) []restRoute {
	routes := []restRoute{
		{http.MethodGet, "/financials/income-statements", rt.statementRoute(
			"get_income_statement",
			"as_reported is not supported; use the normalized income-statements endpoint",
		)},
		{http.MethodGet, "/financials/balance-sheets", rt.statementRoute(
			"get_balance_sheet",
			"as_reported is not supported; use the normalized balance-sheets endpoint",
		)},
		{http.MethodGet, "/financials/cash-flow-statements", rt.statementRoute(
			"get_cash_flow_statement",
			"as_reported is not supported; use the normalized cash-flow-statements endpoint",
		)},
		{http.MethodGet, "/financial-metrics", rt.financialMetrics},
		{http.MethodGet, "/financial-metrics/snapshot", rt.financialMetricsSnapshot},
		{http.MethodGet, "/earnings", rt.earnings},
		{http.MethodGet, "/filings", rt.filings},
		{http.MethodGet, "/prices", rt.prices},
		{http.MethodGet, "/prices/snapshot", rt.priceSnapshot},
		{http.MethodGet, "/news", rt.news},
		{http.MethodGet, "/insider-trades", rt.insiderTrades},
		{http.MethodPost, "/financials/search/screener", rt.screener},
		{http.MethodGet, "/financials/search/screener/filters", rt.screenerFilters},
		{http.MethodGet, "/company/facts", rt.companyFacts},
		{http.MethodGet, "/filings/items", rt.filingItems},
	}
	for _, path := range notImplementedPaths {
		routes = append(routes, restRoute{http.MethodGet, path, notImplemented})
	}
	return routes
}

func notImplemented(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	writeFDError(w, http.StatusOK, "not_implemented", notImplementedMessage)
}

// ---- Financial statements (income / balance / cash flow) ----

var statementDateFilterNames = []string{
	"report_period", "report_period_gte", "report_period_lte", "report_period_gt", "report_period_lt",
}

func (rt *restAPI) statementRoute(tool, asReportedMessage string) routeHandler {
	return func(w http.ResponseWriter, r *http.Request, id callerIdentity) {
		q := r.URL.Query()
		asReported, err := queryBool(q, "as_reported", false)
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if asReported {
			writeFDError(w, http.StatusBadRequest, "bad_request", asReportedMessage)
			return
		}
		limit, err := queryInt(q, "limit", 4)
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		args := map[string]any{
			"period": queryStringDefault(q, "period", "annual"),
			"limit":  float64(limit),
		}
		putQueryString(args, q, "ticker")
		for _, name := range statementDateFilterNames {
			putQueryString(args, q, name)
		}
		rt.callAndRespond(w, r, id, tool, args, nil)
	}
}

// ---- Financial metrics ----

func (rt *restAPI) financialMetrics(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 4)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{
		"period": queryStringDefault(q, "period", "annual"),
		"limit":  float64(limit),
	}
	putQueryString(args, q, "ticker")
	for _, name := range statementDateFilterNames {
		putQueryString(args, q, name)
	}
	rt.callAndRespond(w, r, id, "get_financial_metrics", args, nil)
}

func (rt *restAPI) financialMetricsSnapshot(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	args := map[string]any{}
	putQueryString(args, r.URL.Query(), "ticker")
	rt.callAndRespond(w, r, id, "get_financial_metrics_snapshot", args, nil)
}

// ---- Earnings ----

func (rt *restAPI) earnings(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 1)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{"limit": float64(limit)}
	putQueryString(args, q, "ticker")
	rt.callAndRespond(w, r, id, "get_earnings", args, nil)
}

// ---- Filings ----

func (rt *restAPI) filings(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 10)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{"limit": float64(limit)}
	putQueryString(args, q, "ticker")
	putQueryString(args, q, "cik")
	if types := q["filing_type"]; len(types) > 0 {
		// go/service's generic arg coercion reads array arguments as
		// []any (matching how encoding/json decodes a JSON array), so a
		// native []string here would fail its type assertion.
		anyTypes := make([]any, len(types))
		for i, ft := range types {
			anyTypes[i] = ft
		}
		args["filing_type"] = anyTypes
	}
	rt.callAndRespond(w, r, id, "get_filings", args, nil)
}

// ---- Prices ----

func (rt *restAPI) prices(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	multiplier, err := queryInt(q, "interval_multiplier", 1)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if multiplier != 1 {
		writeFDError(w, http.StatusBadRequest, "bad_request",
			"interval_multiplier must be 1 (multi-bar intervals are not implemented)")
		return
	}
	ticker := strings.ToUpper(strings.TrimSpace(q.Get("ticker")))
	if ticker == "" {
		writeFDError(w, http.StatusBadRequest, "bad_request", "ticker is required")
		return
	}
	args := map[string]any{
		"ticker":   ticker,
		"interval": queryStringDefault(q, "interval", "day"),
	}
	putQueryString(args, q, "start_date")
	putQueryString(args, q, "end_date")
	rt.callAndRespond(w, r, id, "get_stock_prices", args, map[string]any{"ticker": ticker})
}

func (rt *restAPI) priceSnapshot(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	ticker := strings.TrimSpace(r.URL.Query().Get("ticker"))
	if ticker == "" {
		writeFDError(w, http.StatusBadRequest, "bad_request", "ticker is required")
		return
	}
	args := map[string]any{"ticker": ticker}
	rt.callAndRespond(w, r, id, "get_stock_price", args, nil)
}

// ---- News ----

func (rt *restAPI) news(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 5)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{"limit": float64(limit)}
	putQueryString(args, q, "ticker")
	rt.callAndRespond(w, r, id, "get_news", args, nil)
}

// ---- Insider trades ----

func (rt *restAPI) insiderTrades(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 10)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{"limit": float64(limit)}
	putQueryString(args, q, "ticker")
	putQueryString(args, q, "name")
	putQueryString(args, q, "filing_date")
	putQueryString(args, q, "filing_date_gte")
	putQueryString(args, q, "filing_date_lte")
	rt.callAndRespond(w, r, id, "get_insider_trades", args, nil)
}

// ---- Screener ----

type screenerRequest struct {
	Filters []map[string]any `json:"filters"`
	Limit   int              `json:"limit"`
}

func (rt *restAPI) screener(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	var req screenerRequest
	req.Limit = 10
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", "request body must be valid JSON")
		return
	}
	if req.Limit < 1 {
		writeFDError(w, http.StatusBadRequest, "bad_request", "limit must be at least 1")
		return
	}
	// go/service's generic arg coercion reads "filters" as []any of
	// map[string]any (matching a JSON-decoded array), so the typed
	// []map[string]any from req.Filters is re-boxed here.
	filters := make([]any, len(req.Filters))
	for i, f := range req.Filters {
		filters[i] = f
	}
	args := map[string]any{"filters": filters, "limit": float64(req.Limit)}
	rt.callAndRespond(w, r, id, "screen_stocks", args, nil)
}

func (rt *restAPI) screenerFilters(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	rt.callAndRespond(w, r, id, "list_stock_screener_filters", map[string]any{}, nil)
}

// ---- Company facts ----

func (rt *restAPI) companyFacts(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	args := map[string]any{}
	putQueryString(args, q, "ticker")
	putQueryString(args, q, "cik")
	rt.callAndRespond(w, r, id, "get_company_facts", args, nil)
}

// ---- Filing items ----

func (rt *restAPI) filingItems(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	ticker := strings.TrimSpace(q.Get("ticker"))
	filingType := strings.TrimSpace(q.Get("filing_type"))
	if ticker == "" {
		writeFDError(w, http.StatusBadRequest, "bad_request", "ticker is required")
		return
	}
	if filingType == "" {
		writeFDError(w, http.StatusBadRequest, "bad_request", "filing_type is required")
		return
	}
	yearRaw := q.Get("year")
	if yearRaw == "" {
		writeFDError(w, http.StatusBadRequest, "bad_request", "year is required")
		return
	}
	year, err := strconv.Atoi(yearRaw)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", "year must be an integer")
		return
	}
	includeExhibits, err := queryBool(q, "include_exhibits", false)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{
		"ticker":           ticker,
		"filing_type":      filingType,
		"year":             float64(year),
		"include_exhibits": includeExhibits,
	}
	if q.Get("quarter") != "" {
		quarter, err := strconv.Atoi(q.Get("quarter"))
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "bad_request", "quarter must be an integer")
			return
		}
		args["quarter"] = float64(quarter)
	}
	putQueryString(args, q, "item")
	putQueryString(args, q, "accession_number")
	rt.callAndRespond(w, r, id, "get_filing_items", args, nil)
}

// ---- Shared call + response plumbing ----

// callAndRespond runs tool with args, mapping a Caller error to an FD error
// response, else writing the FD-shaped success body per Result.
func (rt *restAPI) callAndRespond(
	w http.ResponseWriter, r *http.Request, id callerIdentity, tool string, args map[string]any, extra map[string]any,
) {
	result, err := rt.caller.Call(r.Context(), id.monidAPIKey, tool, args)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	respond(w, r, result, extra)
}

// respond writes result as the REST response body: unwrapped when
// WrapperKey is empty, else wrapped under WrapperKey with cursor pagination
// applied when Paginate is true.
func respond(w http.ResponseWriter, r *http.Request, result Result, extra map[string]any) {
	if result.WrapperKey == "" {
		writeJSON(w, http.StatusOK, result.Value)
		return
	}

	if !result.Paginate {
		writeJSON(w, http.StatusOK, wrapBody(result.WrapperKey, result.Value, extra))
		return
	}

	offset, err := cursorOffset(r.URL.Query().Get("cursor"))
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "invalid_cursor", "cursor is not a valid opaque pagination token")
		return
	}
	pageSize := pageSizeFor(result.WrapperKey)
	page, hasMore := paginateValue(result.Value, offset, pageSize)
	body := wrapBody(result.WrapperKey, page, extra)
	if hasMore {
		body["next_page_url"] = nextPageURL(r, encodeCursor(offset+pageSize))
	}
	writeJSON(w, http.StatusOK, body)
}

func wrapBody(key string, value any, extra map[string]any) map[string]any {
	body := make(map[string]any, len(extra)+2)
	for k, v := range extra {
		body[k] = v
	}
	body[key] = value
	return body
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ---- Cursor pagination ----

// defaultPageSize is the REST page size for every paginated list route
// except prices, matching compat.py's DEFAULT_PAGE_SIZE.
const defaultPageSize = 10

// pricesPageSize is the REST page size for /prices, matching compat.py's
// PRICES_PAGE_SIZE.
const pricesPageSize = 100

func pageSizeFor(wrapperKey string) int {
	if wrapperKey == "prices" {
		return pricesPageSize
	}
	return defaultPageSize
}

type cursorPayload struct {
	Offset int `json:"o"`
}

// encodeCursor builds an opaque base64url pagination token from an offset.
func encodeCursor(offset int) string {
	raw, _ := json.Marshal(cursorPayload{Offset: offset})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// cursorOffset decodes an opaque cursor into a record offset. An empty
// cursor decodes to offset 0.
func cursorOffset(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("cursor is not valid base64url")
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, fmt.Errorf("cursor payload is not valid")
	}
	if payload.Offset < 0 {
		return 0, fmt.Errorf("cursor offset must be non-negative")
	}
	return payload.Offset, nil
}

// nextPageURL builds the absolute continuation link for cursor, using the
// inbound request's own scheme and host (never a configured upstream base
// URL), so pagination always points back at this deployment.
func nextPageURL(r *http.Request, cursor string) string {
	values := url.Values{"cursor": {cursor}}
	return requestBaseURL(r) + r.URL.Path + "?" + values.Encode()
}

// paginateValue slices a Result.Value (which must be a slice) into one
// page of at most pageSize records starting at offset, reflecting the
// original element type so field order and typing survive re-encoding.
func paginateValue(value any, offset, pageSize int) (page any, hasMore bool) {
	v := reflect.ValueOf(value)
	if !v.IsValid() || v.Kind() != reflect.Slice {
		// Defensive: a non-slice Paginate=true value is not paginated.
		return value, false
	}
	n := v.Len()
	if offset < 0 {
		offset = 0
	}
	if offset > n {
		offset = n
	}
	end := offset + pageSize
	if end > n {
		end = n
	}
	if end <= offset {
		return reflect.MakeSlice(v.Type(), 0, 0).Interface(), false
	}
	return v.Slice(offset, end).Interface(), end < n
}

// ---- Query parameter helpers ----

func queryStringDefault(q url.Values, name, def string) string {
	if v := q.Get(name); v != "" {
		return v
	}
	return def
}

func putQueryString(args map[string]any, q url.Values, name string) {
	if v := q.Get(name); v != "" {
		args[name] = v
	}
}

func queryInt(q url.Values, name string, def int) (int, error) {
	raw := q.Get(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return n, nil
}

func queryBool(q url.Values, name string, def bool) (bool, error) {
	raw := q.Get(name)
	if raw == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean", name)
	}
	return b, nil
}
