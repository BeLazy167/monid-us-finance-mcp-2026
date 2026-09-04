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
// not implement. /kpi/metrics/tickers and /institutional-holdings/tickers
// used to stub here on the grounds that get_kpi_metrics/
// get_institutional_holdings have no enumerable per-dataset universe;
// they are now wired to the shared accept-universe coverage list instead
// (Service.ListKPITickers/ListInstitutionalHoldingsTickers - see
// go/service/coverage.go's doc comment), worded as the tickers the route
// ACCEPTS, not a coverage claim. The four ownership-state paths
// (/insider-ownership, /beneficial-ownership, /activist-ownership,
// /institutional-holdings/investors) also used to stub here, on the
// then-true grounds that their tools had no working service handler;
// all four now have one and are wired below. The paths left here have no
// such answer, for the reason noted beside each. Each answers 200
// {"error": "not_implemented", "message": ...} once authorized, at zero
// cost, and never reaches Caller.
var notImplementedPaths = []string{
	// get_kpi_metrics itself has no fixed per-ticker/per-sector *data*
	// universe (it extracts on demand from whichever filing a ticker
	// has); /kpi/metrics/tickers now answers the accept-universe instead
	// (see above). /kpi/metrics/sectors has no such accept-universe
	// fallback: sector is not a dimension the shared catalog carries at
	// all (catalogTickerUniverse: ticker/companyName/country/
	// countryName only), so there is nothing honest to enumerate.
	"/kpi/metrics/sectors",
	// get_index_fund resolves holdings via a live web search per ticker
	// (indexfund.go); knownIssuerDomains is a search-ranking hint for a
	// handful of tickers, not an exhaustive "tickers we can serve" list,
	// so publishing it as a coverage catalog would overstate what this
	// server actually supports.
	"/index-funds/tickers",
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

		// ---- All financials / line-item search (get_all_financials, search_line_items) ----
		{http.MethodGet, "/financials", rt.allFinancials},
		{http.MethodPost, "/financials/search/line-items", rt.searchLineItems},

		// ---- Coverage lists: tickers each route ACCEPTS (see coverageRoute) ----
		{http.MethodGet, "/company/facts/tickers", rt.coverageRoute("list_company_facts_tickers")},
		{http.MethodGet, "/earnings/tickers", rt.coverageRoute("list_earnings_tickers")},
		{http.MethodGet, "/filings/tickers", rt.coverageRoute("list_filings_tickers")},
		{http.MethodGet, "/financial-metrics/snapshot/tickers", rt.coverageRoute("list_metrics_snapshot_tickers")},
		{http.MethodGet, "/prices/tickers", rt.coverageRoute("list_prices_tickers")},
		{http.MethodGet, "/prices/snapshot/tickers", rt.coverageRoute("list_price_snapshot_tickers")},
		{http.MethodGet, "/institutional-holdings/tickers", rt.coverageRoute("list_institutional_holdings_tickers")},
		{http.MethodGet, "/kpi/metrics/tickers", rt.coverageRoute("list_kpi_tickers")},

		// ---- CIK enumeration, sourced free from SEC (see secciks.go) ----
		{http.MethodGet, "/filings/ciks", rt.cikRoute("list_filings_ciks")},
		{http.MethodGet, "/company/facts/ciks", rt.cikRoute("list_company_facts_ciks")},

		// ---- Static catalogs, zero paid calls (list_filing_types / list_filing_item_types) ----
		{http.MethodGet, "/filings/types", rt.filingTypes},
		{http.MethodGet, "/filings/items/types", rt.filingItemTypes},

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
		// /macro/interest-rates/banks calls Service.ListInterestRateBanks
		// (list_interest_rate_banks), which derives its answer from
		// bankSpecs (go/service/interestrates.go) directly: this route
		// used to hand-type its own copy of the bank code list, which
		// could silently drift from bankSpecs if a bank were ever added
		// or removed there. It still makes no Monid call.
		{http.MethodGet, "/macro/interest-rates/banks", rt.interestRateBanks},

		// ---- Index fund holdings (get_index_fund) ----
		{http.MethodGet, "/index-funds", rt.indexFunds},

		// ---- 13F institutional holdings (get_institutional_holdings) ----
		{http.MethodGet, "/institutional-holdings", rt.institutionalHoldings},
		{http.MethodGet, "/institutional-holdings/investors", rt.institutionalInvestors},

		// ---- Ownership state (get_insider_ownership / get_beneficial_ownership) ----
		{http.MethodGet, "/insider-ownership", rt.insiderOwnership},
		{http.MethodGet, "/beneficial-ownership", rt.beneficialOwnershipRoute("", beneficialOwnersWrapperKey)},
		{http.MethodGet, "/activist-ownership", rt.beneficialOwnershipRoute("activist", activistOwnersWrapperKey)},

		// ---- Market-wide price snapshot (get_market_snapshot) ----
		{http.MethodGet, "/prices/snapshot/market", rt.marketSnapshot},

		// ---- SEC registration statements (get_ipos) ----
		{http.MethodGet, "/ipos", rt.ipos},

		// ---- Asynchronous filing-item requests (see filingItemsRequest) ----
		{http.MethodGet, "/filings/items/requests/{request_id}", rt.filingItemsRequest},

		// ---- As-filed statement hierarchies (get_as_reported) ----
		{http.MethodGet, "/financials/as-reported", rt.asReportedRoute("all")},
		{http.MethodGet, "/financials/income-statements/as-reported", rt.asReportedRoute("income")},
		{http.MethodGet, "/financials/balance-sheets/as-reported", rt.asReportedRoute("balance")},
		{http.MethodGet, "/financials/cash-flow-statements/as-reported", rt.asReportedRoute("cash")},
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

// interestRateBanks answers /macro/interest-rates/banks via
// Service.ListInterestRateBanks (Caller.Capability, not Call): the static
// coverage list of banks get_interest_rates can return a rate for,
// derived from bankSpecs itself so this route can never drift from what
// get_interest_rates actually scrapes. It still makes no Monid call.
func (rt *restAPI) interestRateBanks(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	rt.callCapabilityAndRespond(w, r, id, "list_interest_rate_banks", map[string]any{}, nil)
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

// ---- Ownership state (get_insider_ownership / get_beneficial_ownership) ----

// filingDateFilterNames is the five-way filing_date filter group the
// ownership routes forward verbatim, mirroring statementDateFilterNames'
// role for the report_period group. /insider-trades forwards only three of
// these because get_insider_trades' own schema defines only three.
var filingDateFilterNames = []string{
	"filing_date", "filing_date_gte", "filing_date_lte", "filing_date_gt", "filing_date_lt",
}

// insiderOwnership answers /insider-ownership via get_insider_ownership:
// each insider's latest post-transaction shares_owned figure for one
// ticker (go/service/insiderownership.go). ticker is required by the tool
// itself, so an omitted ticker becomes that tool's own zero-cost
// bad_request rather than a check duplicated here. form_type is forwarded
// rather than rejected here for the same reason: the tool rejects it with
// a message naming the underlying feed's actual limitation.
func (rt *restAPI) insiderOwnership(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 10)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{"limit": float64(limit)}
	putQueryString(args, q, "ticker")
	putQueryString(args, q, "name")
	putQueryString(args, q, "form_type")
	for _, name := range filingDateFilterNames {
		putQueryString(args, q, name)
	}
	rt.callAndRespond(w, r, id, "get_insider_ownership", args, nil)
}

// beneficialOwnersWrapperKey is the envelope key get_beneficial_ownership
// itself sets, and the one /beneficial-ownership answers with.
const beneficialOwnersWrapperKey = "beneficial_owners"

// activistOwnersWrapperKey is the envelope key Financial Datasets uses on
// /activist-ownership (ActivistOwnershipResponse), which differs from
// /beneficial-ownership's despite both carrying the same record type.
const activistOwnersWrapperKey = "activist_owners"

// beneficialOwnershipRoute builds the handler both 13D/13G stake routes
// share. Financial Datasets defines /activist-ownership as exactly the
// activist (Schedule 13D) subset of /beneficial-ownership, which is why it
// publishes no `type` parameter of its own; pinnedType carries that, and
// is "" for /beneficial-ownership, where `type` is the caller's to set.
// A pinned route ignores any caller-supplied type rather than letting a
// ?type=passive turn /activist-ownership into its own opposite.
//
// wrapperKey exists because Financial Datasets does NOT reuse one envelope
// across the two routes: /beneficial-ownership answers "beneficial_owners"
// and /activist-ownership answers "activist_owners", even though both
// carry the same record. The shared tool sets the former, so the activist
// route rewrites it here.
func (rt *restAPI) beneficialOwnershipRoute(pinnedType, wrapperKey string) routeHandler {
	return func(w http.ResponseWriter, r *http.Request, id callerIdentity) {
		q := r.URL.Query()
		limit, err := queryInt(q, "limit", 10)
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		history, err := queryBool(q, "history", false)
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		args := map[string]any{
			"limit":   float64(limit),
			"history": history,
		}
		putQueryString(args, q, "ticker")
		putQueryString(args, q, "filer_cik")
		if pinnedType != "" {
			args["type"] = pinnedType
		} else {
			putQueryString(args, q, "type")
		}
		for _, name := range filingDateFilterNames {
			putQueryString(args, q, name)
		}
		result, err := rt.caller.Call(r.Context(), id.monidAPIKey, "get_beneficial_ownership", args)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		// Only the success list shape gets re-keyed; the tool's own
		// bad_request body (WrapperKey == "") passes through unchanged,
		// exactly as segmentedFinancialsRoute guards its reshape.
		if result.WrapperKey == beneficialOwnersWrapperKey {
			result.WrapperKey = wrapperKey
		}
		respond(w, r, result, nil)
	}
}

// institutionalInvestors answers /institutional-holdings/investors via
// get_institutional_investors: the directory of distinct 13F filers, used
// to discover the filer_cik /institutional-holdings takes. name is an
// optional case-insensitive prefix filter.
func (rt *restAPI) institutionalInvestors(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	args := map[string]any{}
	q := r.URL.Query()
	putQueryString(args, q, "name")
	// ticker is not a Financial Datasets parameter on this route. The
	// underlying 13F feed is keyed on one issuer's CIK and cannot
	// enumerate filers across all issuers, so this server scopes the
	// lookup (see go/service/institutionalinvestors.go). An omitted
	// ticker reaches the tool's own bad_request, which explains why.
	putQueryString(args, q, "ticker")
	rt.callAndRespond(w, r, id, "get_institutional_investors", args, nil)
}

// ---- SEC registration statements (get_ipos) ----

// ---- Asynchronous filing-item requests ----

// filingItemsRequest answers /filings/items/requests/{request_id}.
//
// Financial Datasets can accept a filing-item extraction and hand back a
// request id to poll. This server has no such queue: /filings/items runs
// the extraction inline and returns the items on the same response, so no
// request id it could be given ever existed. Any id is therefore a
// not_found rather than a pending or failed request, which is the honest
// answer - reporting "pending" for work that will never complete would
// leave a client polling forever.
//
// It costs nothing and never reaches Caller.
func (rt *restAPI) filingItemsRequest(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	writeFDError(w, http.StatusNotFound, "not_found",
		"No such filing-items request. This server extracts filing items synchronously: "+
			"/filings/items returns the items directly and never issues a request id to poll.")
}

// ipos answers /ipos via Service.GetIPOs: one issuer's S-1 and S-1/A
// registration statements. ticker is required by the tool itself, and
// classification is forwarded so the caller receives that tool's own
// explanation of why this server cannot classify a filing.
func (rt *restAPI) ipos(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	limit, err := queryInt(q, "limit", 10)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	args := map[string]any{"limit": float64(limit)}
	putQueryString(args, q, "ticker")
	putQueryString(args, q, "cik")
	putQueryString(args, q, "classification")
	for _, name := range filingDateFilterNames {
		putQueryString(args, q, name)
	}
	rt.callCapabilityAndRespond(w, r, id, "get_ipos", args, nil)
}

// ---- Market-wide price snapshot (get_market_snapshot) ----

// marketSnapshot answers /prices/snapshot/market via
// Service.GetMarketSnapshot (Caller.Capability, not Call - it is not one
// of the 27 FD MCP tools). The capability already emits Financial
// Datasets' PriceSnapshotMarketResponse shape, so this route forwards it
// unchanged. Financial Datasets publishes no parameters for this route,
// so there is nothing to read off the query string.
func (rt *restAPI) marketSnapshot(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	rt.callCapabilityAndRespond(w, r, id, "get_market_snapshot", map[string]any{}, nil)
}

// ---- All financials (get_all_financials) ----

// allFinancials mirrors statementRoute's own shape (as_reported rejected
// at zero cost, period/limit/report_period* forwarded), since
// get_all_financials shares get_income_statement/get_balance_sheet/
// get_cash_flow_statement's own parseStatementArgs validation
// (go/service/allfinancials.go). Like those three routes, cik and
// filing_date* are not forwarded here either (see docs/openapi-notes.md's
// documented REST-vs-tool deviations).
func (rt *restAPI) allFinancials(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	q := r.URL.Query()
	asReported, err := queryBool(q, "as_reported", false)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if asReported {
		writeFDError(w, http.StatusBadRequest, "bad_request",
			"as_reported is not supported; use the normalized financials endpoint")
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
	rt.callCapabilityAndRespond(w, r, id, "get_all_financials", args, nil)
}

// ---- Line-item search (search_line_items) ----

// searchLineItemsRequest is the POST /financials/search/line-items body.
// line_items/tickers have no server-side default (search_line_items
// itself 400s when either is empty); period/limit mirror
// search_line_items' own defaults ("ttm", 1) so an omitted field in the
// JSON body still reaches the capability with the same default a caller
// who never set the key would get.
type searchLineItemsRequest struct {
	LineItems []string `json:"line_items"`
	Tickers   []string `json:"tickers"`
	Period    string   `json:"period"`
	Limit     int      `json:"limit"`
}

func (rt *restAPI) searchLineItems(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	req := searchLineItemsRequest{Period: "ttm", Limit: 1}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", "request body must be valid JSON")
		return
	}
	args := map[string]any{
		"period": req.Period,
		"limit":  float64(req.Limit),
	}
	// go/service's generic arg coercion reads array arguments as []any
	// (matching how encoding/json decodes a JSON array into `any`), so
	// the typed []string fields from req are re-boxed here, the same way
	// screener re-boxes req.Filters.
	if req.LineItems != nil {
		lineItems := make([]any, len(req.LineItems))
		for i, v := range req.LineItems {
			lineItems[i] = v
		}
		args["line_items"] = lineItems
	}
	if req.Tickers != nil {
		tickers := make([]any, len(req.Tickers))
		for i, v := range req.Tickers {
			tickers[i] = v
		}
		args["tickers"] = tickers
	}
	rt.callCapabilityAndRespond(w, r, id, "search_line_items", args, nil)
}

// ---- Coverage lists: tickers each route ACCEPTS, not a coverage claim ----
//
// All eight list*Tickers capabilities (go/service/coverage.go) answer from
// the very same ~3,227-ticker US equity catalog: the tickers a route will
// ACCEPT as input, never a claim that every one of them has data for that
// particular dataset. Every response/description in this file and in
// docs/openapi.json is worded that way deliberately.

// coverageDefaultLimit/coverageMaxLimit mirror go/service/coverage.go's own
// bounds (duplicated here as REST-facing input validation only; go/httpapi
// deliberately does not import go/service - see router.go's Caller doc).
// This layer always asks the capability for the full universe
// (coverageMaxLimit, comfortably above the ~3,227-ticker universe) so it
// can apply its own cursor pagination without ever asking "total" - which
// must always be the full universe size - to move between pages.
const (
	coverageDefaultLimit = 1000
	coverageMaxLimit     = 5000
)

// coverageBody decodes just enough of one coverage-list Capability
// response ({"resource", "total", "tickers"}, go/service/coverage.go's
// catalogListResponse) to repaginate it: go/httpapi deliberately does not
// import go/service's unexported orderedJSONObject type, so this reflects
// the shape via JSON the same way reshapeSegmentRecord does for
// get_segmented_financials.
type coverageBody struct {
	Resource string   `json:"resource"`
	Total    int      `json:"total"`
	Tickers  []string `json:"tickers"`
}

// coverageRoute builds one of the eight list*Tickers coverage-list REST
// handlers. limit/cursor are validated and applied entirely in this
// layer: the underlying capability is always asked for the full universe
// (see coverageMaxLimit above), which also means two coverage routes (or
// two pages of the same route) requested inside the shared TTL cache
// window cost exactly one Monid-adjacent catalog fetch between them.
func (rt *restAPI) coverageRoute(capability string) routeHandler {
	return func(w http.ResponseWriter, r *http.Request, id callerIdentity) {
		q := r.URL.Query()
		limit, err := queryInt(q, "limit", coverageDefaultLimit)
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if limit < 1 || limit > coverageMaxLimit {
			writeFDError(w, http.StatusBadRequest, "bad_request",
				fmt.Sprintf("limit must be between 1 and %d", coverageMaxLimit))
			return
		}
		offset, err := cursorOffset(q.Get("cursor"))
		if err != nil {
			writeFDError(w, http.StatusBadRequest, "invalid_cursor", "cursor is not a valid opaque pagination token")
			return
		}
		result, err := rt.caller.Capability(r.Context(), id.monidAPIKey, capability, map[string]any{"limit": float64(coverageMaxLimit)})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		raw, merr := json.Marshal(result.Value)
		if merr != nil {
			writeFDError(w, http.StatusBadGateway, "upstream_schema_changed", "coverage-list response is not valid JSON")
			return
		}
		var body coverageBody
		if uerr := json.Unmarshal(raw, &body); uerr != nil {
			writeFDError(w, http.StatusBadGateway, "upstream_schema_changed", "coverage-list response did not match the expected shape")
			return
		}
		tickers := make([]any, len(body.Tickers))
		for i, t := range body.Tickers {
			tickers[i] = t
		}
		page, hasMore := paginateValue(tickers, offset, limit)
		out := map[string]any{
			"resource": body.Resource,
			"total":    body.Total,
			"tickers":  page,
		}
		if hasMore {
			// Unlike nextPageURL (used by every other paginated route,
			// where the REST page size is a fixed per-resource constant -
			// see pageSizeFor), a coverage list's page size IS its own
			// `limit` query parameter, so the continuation link must
			// carry `limit` forward too: dropping it would silently
			// reset the caller's page size back to coverageDefaultLimit
			// on every follow-up request.
			values := url.Values{"cursor": {encodeCursor(offset + limit)}, "limit": {strconv.Itoa(limit)}}
			out["next_page_url"] = requestBaseURL(r) + r.URL.Path + "?" + values.Encode()
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// ---- Static catalogs, zero paid calls (list_filing_types / list_filing_item_types) ----

// ---- CIK enumeration (list_filings_ciks / list_company_facts_ciks) ----

// cikRoute answers the two CIK enumeration routes. Unlike coverageRoute,
// this layer applies no pagination and no limit: Financial Datasets
// returns these lists whole (verified live 2026-09-04 - /filings/ciks came
// back as all 10,412 entries with no next_page_url), so paginating here
// would break a client that reads the array in one pass.
func (rt *restAPI) cikRoute(capability string) routeHandler {
	return func(w http.ResponseWriter, r *http.Request, id callerIdentity) {
		rt.callCapabilityAndRespond(w, r, id, capability, map[string]any{}, nil)
	}
}

// filingTypes answers /filings/types via Service.ListFilingTypes
// (Caller.Capability, not Call): the static filing_type enum
// get_filings/get_filing_items validate against. It makes no Monid call.
func (rt *restAPI) filingTypes(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	rt.callCapabilityAndRespond(w, r, id, "list_filing_types", map[string]any{}, nil)
}

// filingItemTypes answers /filings/items/types via
// Service.ListFilingItemTypes (Caller.Capability, not Call): the static
// SEC form-instruction item catalog get_filing_items reads from
// (go/providers/filingitems.go). filing_type is optional; when omitted,
// all three supported forms (10-K/10-Q/8-K) are returned. It makes no
// Monid call.
func (rt *restAPI) filingItemTypes(w http.ResponseWriter, r *http.Request, id callerIdentity) {
	args := map[string]any{}
	putQueryString(args, r.URL.Query(), "filing_type")
	rt.callCapabilityAndRespond(w, r, id, "list_filing_item_types", args, nil)
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

// callCapabilityAndRespond is callAndRespond's twin for the
// Caller.Capability surface (go/service/capabilities.go's 13 non-tool
// capabilities), used by every route in this file that is not one of the
// 27 Financial Datasets MCP tools.
func (rt *restAPI) callCapabilityAndRespond(
	w http.ResponseWriter, r *http.Request, id callerIdentity, name string, args map[string]any, extra map[string]any,
) {
	result, err := rt.caller.Capability(r.Context(), id.monidAPIKey, name, args)
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
