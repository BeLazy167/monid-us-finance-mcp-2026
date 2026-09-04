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
// not implement. Every path here was checked against go/service/tools.go
// for a cheap, zero-cost coverage-list answer first (see
// docs/openapi-notes.md); none of the eight has one, for the reason noted
// beside it. Each answers 200 {"error": "not_implemented", "message": ...}
// once authorized, at zero cost, and never reaches Caller.
var notImplementedPaths = []string{
	// get_kpi_metrics has no fixed ticker/sector universe: it extracts
	// on demand from whichever filing a ticker has, so there is no
	// catalog to enumerate without either a paid call per candidate
	// ticker or fabricating one.
	"/kpi/metrics/tickers",
	"/kpi/metrics/sectors",
	// get_index_fund resolves holdings via a live web search per ticker
	// (indexfund.go); knownIssuerDomains is a search-ranking hint for a
	// handful of tickers, not an exhaustive "tickers we can serve" list,
	// so publishing it as a coverage catalog would overstate what this
	// server actually supports.
	"/index-funds/tickers",
	// get_institutional_holdings takes any ticker/CIK the SECForm4
	// provider recognizes; this server holds no local list of covered
	// tickers or investors to enumerate.
	"/institutional-holdings/tickers",
	"/institutional-holdings/investors",
	// The four ownership-state tools these paths would call
	// (get_insider_ownership, get_beneficial_ownership,
	// get_beneficial_owners, get_institutional_investors) are themselves
	// notImplementedHandler in go/service/tools.go: there is no working
	// service method to wire yet.
	"/insider-ownership",
	"/beneficial-ownership",
	"/activist-ownership",
}

// restRoutes builds the full REST route table, ported route-for-route from
// src/monid_finance_mcp/rest_api.py, plus every route wired directly onto
// a working go/service/tools.go handler once it was verified against that
// handler and its tool schema (see docs/openapi-notes.md).
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

		// ---- Segmented financials (get_segmented_financials) ----
		{http.MethodGet, "/financials/segments", rt.segmentedFinancialsRoute(segmentVariantCombined)},
		{http.MethodGet, "/financials/income-statements/segments", rt.segmentedFinancialsRoute(segmentVariantIncomeStatement)},
		{http.MethodGet, "/financials/balance-sheets/segments", rt.segmentedFinancialsRoute(segmentVariantBalanceSheet)},
		{http.MethodGet, "/financials/cash-flow-statements/segments", rt.segmentedFinancialsRoute(segmentVariantCashFlow)},

		// ---- KPI extraction (get_kpi_metrics / get_kpi_guidance / get_kpi_non_gaap) ----
		{http.MethodGet, "/kpi/metrics", rt.kpiRoute("get_kpi_metrics")},
		{http.MethodGet, "/kpi/guidance", rt.kpiRoute("get_kpi_guidance")},
		{http.MethodGet, "/kpi/non-gaap", rt.kpiRoute("get_kpi_non_gaap")},

		// ---- Central bank interest rates (get_interest_rates) ----
		// Both routes call the same zero-parameter tool and return the same
		// live-scraped snapshot: this server has no historical time series,
		// so /macro/interest-rates (FD's historical route) answers with
		// today's rate per bank instead of a stub, and
		// /macro/interest-rates/snapshot (FD's real-time route) answers with
		// the same call, which is already "latest observation only".
		{http.MethodGet, "/macro/interest-rates", rt.interestRates},
		{http.MethodGet, "/macro/interest-rates/snapshot", rt.interestRates},
		// /macro/interest-rates/banks needs no tool call: it is the static
		// list of central banks bankSpecs (go/service/interestrates.go)
		// scrapes, so it is served directly at zero cost.
		{http.MethodGet, "/macro/interest-rates/banks", interestRateBanks},

		// ---- Index fund holdings (get_index_fund) ----
		{http.MethodGet, "/index-funds", rt.indexFunds},

		// ---- 13F institutional holdings (get_institutional_holdings) ----
		{http.MethodGet, "/institutional-holdings", rt.institutionalHoldings},
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

// ---- Segmented financials (get_segmented_financials) ----

// segmentVariant selects how a get_segmented_financials record is shaped
// for one of the four segments REST routes: the combined route returns
// the tool's record unchanged; the three per-statement routes reshape it
// to match Financial Datasets' flat per-statement schema (SegmentMetadata
// plus that statement's own fields), never adding a field this server has
// no data for.
type segmentVariant int

const (
	// segmentVariantCombined matches /financials/segments: the tool's
	// record (currently income_statement only; see segmentedfinancials.go)
	// is returned as-is, matching SegmentedFinancialsResponse.
	segmentVariantCombined segmentVariant = iota
	// segmentVariantIncomeStatement matches /financials/income-statements/segments:
	// the record's income_statement.revenue is hoisted to the top level,
	// matching IncomeStatementSegments. operating_income and depreciation
	// are declared for schema parity but always omitted: segmentedfinancials.go's
	// extraction schema never asks for them.
	segmentVariantIncomeStatement
	// segmentVariantBalanceSheet matches /financials/balance-sheets/segments:
	// go/service's segment extraction (segmentedfinancials.go) only ever
	// asks the filing for product/geographic net sales (income-statement
	// revenue), so assets/goodwill/long_lived_assets are declared for
	// schema parity but always omitted, never fabricated.
	segmentVariantBalanceSheet
	// segmentVariantCashFlow matches /financials/cash-flow-statements/segments:
	// same reasoning as segmentVariantBalanceSheet - capital_expenditure is
	// declared but never sourced today.
	segmentVariantCashFlow
)

// segmentBreakdown mirrors Financial Datasets' SegmentBreakdown schema
// (label, value), matching go/service/segmentedfinancials.go's segmentRow.
type segmentBreakdown struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

// segmentCategory mirrors Financial Datasets' SegmentCategory schema.
type segmentCategory struct {
	Product []segmentBreakdown `json:"product,omitempty"`
	Segment []segmentBreakdown `json:"segment,omitempty"`
}

// segmentMetadataView mirrors the fields Financial Datasets' SegmentMetadata
// schema declares that this server actually sources today (ticker,
// report_period, fiscal_period, period, accession_number, filing_url); it
// omits SegmentMetadata's "currency" field since nothing in this port ever
// sets one.
type segmentMetadataView struct {
	Ticker          *string `json:"ticker,omitempty"`
	ReportPeriod    *string `json:"report_period,omitempty"`
	FiscalPeriod    *string `json:"fiscal_period,omitempty"`
	Period          *string `json:"period,omitempty"`
	AccessionNumber *string `json:"accession_number,omitempty"`
	FilingURL       *string `json:"filing_url,omitempty"`
}

// incomeStatementSegmentsRow mirrors Financial Datasets' IncomeStatementSegments.
type incomeStatementSegmentsRow struct {
	segmentMetadataView
	Revenue         *segmentCategory `json:"revenue,omitempty"`
	OperatingIncome *segmentCategory `json:"operating_income,omitempty"`
	Depreciation    *segmentCategory `json:"depreciation,omitempty"`
}

// balanceSheetSegmentsRow mirrors Financial Datasets' BalanceSheetSegments.
type balanceSheetSegmentsRow struct {
	segmentMetadataView
	Assets          *segmentCategory `json:"assets,omitempty"`
	Goodwill        *segmentCategory `json:"goodwill,omitempty"`
	LongLivedAssets *segmentCategory `json:"long_lived_assets,omitempty"`
}

// cashFlowSegmentsRow mirrors Financial Datasets' CashFlowStatementSegments.
type cashFlowSegmentsRow struct {
	segmentMetadataView
	CapitalExpenditure *segmentCategory `json:"capital_expenditure,omitempty"`
}

// incomeStatementSegmentsView is the shape reshapeSegmentRecord decodes a
// get_segmented_financials record's "income_statement" object into.
type incomeStatementSegmentsView struct {
	Revenue         *segmentCategory `json:"revenue,omitempty"`
	OperatingIncome *segmentCategory `json:"operating_income,omitempty"`
	Depreciation    *segmentCategory `json:"depreciation,omitempty"`
}

func (rt *restAPI) segmentedFinancialsRoute(variant segmentVariant) routeHandler {
	return func(w http.ResponseWriter, r *http.Request, id callerIdentity) {
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
		result, err := rt.caller.Call(r.Context(), id.monidAPIKey, "get_segmented_financials", args)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		// Only the success list shape gets reshaped; a not_found FD error
		// response (WrapperKey == "") passes straight through unchanged.
		if variant != segmentVariantCombined && result.WrapperKey == "segmented_financials" {
			result.Value = reshapeSegmentRecords(result.Value, variant)
		}
		respond(w, r, result, nil)
	}
}

// reshapeSegmentRecords reshapes every record in a get_segmented_financials
// list result for one of the three per-statement segments routes. value's
// concrete slice element type is reflected rather than assumed (mirroring
// paginateValue below), since the real service returns []any but tests may
// configure a concretely typed slice.
func reshapeSegmentRecords(value any, variant segmentVariant) any {
	v := reflect.ValueOf(value)
	if !v.IsValid() || v.Kind() != reflect.Slice {
		return value
	}
	out := make([]any, v.Len())
	for i := 0; i < v.Len(); i++ {
		out[i] = reshapeSegmentRecord(v.Index(i).Interface(), variant)
	}
	return out
}

// reshapeSegmentRecord decodes one get_segmented_financials record (via its
// JSON encoding, since go/httpapi deliberately does not import go/service's
// unexported record type - see router.go's Caller interface note) and
// rebuilds it in the target statement-specific shape.
func reshapeSegmentRecord(item any, variant segmentVariant) any {
	raw, err := json.Marshal(item)
	if err != nil {
		return item
	}
	var meta segmentMetadataView
	if err := json.Unmarshal(raw, &meta); err != nil {
		return item
	}
	switch variant {
	case segmentVariantIncomeStatement:
		var wrapper struct {
			IncomeStatement *incomeStatementSegmentsView `json:"income_statement"`
		}
		_ = json.Unmarshal(raw, &wrapper)
		row := incomeStatementSegmentsRow{segmentMetadataView: meta}
		if wrapper.IncomeStatement != nil {
			row.Revenue = wrapper.IncomeStatement.Revenue
			row.OperatingIncome = wrapper.IncomeStatement.OperatingIncome
			row.Depreciation = wrapper.IncomeStatement.Depreciation
		}
		return row
	case segmentVariantBalanceSheet:
		return balanceSheetSegmentsRow{segmentMetadataView: meta}
	case segmentVariantCashFlow:
		return cashFlowSegmentsRow{segmentMetadataView: meta}
	default:
		return item
	}
}

// ---- KPI extraction (get_kpi_metrics / get_kpi_guidance / get_kpi_non_gaap) ----

// kpiRoute builds the shared handler for the three KPI extraction routes:
// tool is the go/service tool name; the tool itself sets Result.WrapperKey
// (kpi_metrics / kpi_guidance / kpi_non_gaap), so this handler need not
// repeat it.
func (rt *restAPI) kpiRoute(tool string) routeHandler {
	return func(w http.ResponseWriter, r *http.Request, id callerIdentity) {
		q := r.URL.Query()
		limit, err := queryInt(q, "limit", 4)
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		args := map[string]any{
			"period": queryStringDefault(q, "period", "quarterly"),
			"limit":  float64(limit),
		}
		putQueryString(args, q, "ticker")
		putQueryString(args, q, "metric_name")
		putQueryString(args, q, "report_period_gte")
		putQueryString(args, q, "report_period_lte")
		rt.callAndRespond(w, r, id, tool, args, nil)
	}
}

// ---- Central bank interest rates (get_interest_rates) ----

func (rt *restAPI) interestRates(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	// get_interest_rates takes no parameters (see tool_schemas.json): it
	// always scrapes and returns the current rate for every bank it can
	// reach, so there is nothing to read off the query string here.
	rt.callAndRespond(w, r, id, "get_interest_rates", map[string]any{}, nil)
}

// interestRateBankCodes are the central banks go/service/interestrates.go's
// bankSpecs currently scrapes. Duplicated here (rather than imported) since
// go/httpapi deliberately does not depend on go/service; keep this list in
// sync with bankSpecs if a bank is ever added or removed there.
var interestRateBankCodes = []string{"FED", "ECB", "BOE", "BOJ"}

// interestRateBanks answers /macro/interest-rates/banks directly, with no
// Caller.Call and no Monid spend: it is the static coverage list of banks
// get_interest_rates can return a rate for.
func interestRateBanks(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	banks := make([]any, len(interestRateBankCodes))
	for i, code := range interestRateBankCodes {
		banks[i] = code
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource": "interest_rates",
		"banks":    banks,
	})
}

// ---- Index fund holdings (get_index_fund) ----

func (rt *restAPI) indexFunds(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 50)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	offset, err := queryInt(q, "offset", 0)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{
		"limit":  float64(limit),
		"offset": float64(offset),
	}
	putQueryString(args, q, "ticker")
	putQueryString(args, q, "as_of")
	putQueryString(args, q, "asset_class")
	// get_index_fund has no "holding" (reverse lookup) parameter; a caller
	// passing ?holding=... without ticker gets the tool's own
	// "ticker is required" bad_request, matching every other route's
	// behavior of validating only what the underlying tool schema defines.
	rt.callAndRespond(w, r, id, "get_index_fund", args, nil)
}

// ---- 13F institutional holdings (get_institutional_holdings) ----

func (rt *restAPI) institutionalHoldings(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 10)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{"limit": float64(limit)}
	putQueryString(args, q, "filer_cik")
	putQueryString(args, q, "ticker")
	putQueryString(args, q, "report_period")
	putQueryString(args, q, "report_period_gte")
	putQueryString(args, q, "report_period_lte")
	rt.callAndRespond(w, r, id, "get_institutional_holdings", args, nil)
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
