// Package service is the Go orchestration layer for all 27 Financial
// Datasets tools: the tool -> provider/endpoint map, argument validation
// and FD-schema defaults, the honest rejections, and the composition steps
// that join multiple Monid calls into one Financial Datasets response.
//
// src/monid_finance_mcp/service.py is the executable spec this package
// ports: every provider/endpoint pair, validation order, FD default, honest
// rejection, and composition step here was verified against the live
// Financial Datasets API by that Python implementation. Where this port's
// brief explicitly calls for different behavior (the FD JSON Schema
// defaults in docs/fd-mcp-tool-schemas.json, and the goroutine fan-outs
// described below), that is noted at the call site.
//
// Provenance, cost, and warnings never appear inside a tool response: every
// Monid call attempt (success or failure) is recorded as one row in the
// receipts ledger (fd.ReceiptsLedger) instead, mirroring receipts.py.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/monid"
	"github.com/belazy/monid-finance/providers"
)

// --- provider/endpoint constants (mirrors service.py's module constants) ---

const (
	defillama               = "defillama"
	catalogEndpoint         = "/equities/v1/companies-list"
	summaryEndpoint         = "/equities/v1/summary"
	statementsEndpoint      = "/equities/v1/statements"
	filingsEndpoint         = "/equities/v1/filings"
	ohlcvEndpoint           = "/equities/v1/ohlcv"
	contextDev              = "context.dev"
	newsEndpoint            = "/news/search"
	scrapeEndpoint          = "/web/scrape/markdown"
	scrapeHTMLEndpoint      = "/web/scrape/html"
	indexFundSearchEndpoint = "/web/search"
	secform4                = "secform4"
	insiderEndpoint         = "/search"

	// insiderMaxRows is the most rows the Form 4 feed returns for one query.
	insiderMaxRows           = 15
	institutionalEndpoint    = "/get_institution_holders"
	nasdaq                   = "nasdaq"
	screenerEndpoint         = "/get_stock_screener"
	earningsCalendarEndpoint = "/get_earnings_calendar"
)

// annualForms/quarterlyForms mirror service.py's ANNUAL_FORMS/QUARTERLY_FORMS.
var annualForms = map[string]bool{"10-K": true, "20-F": true}
var quarterlyForms = map[string]bool{"10-Q": true, "6-K": true}

// Config configures a Service. HTTP is a shared *http.Client so its
// connection pool pools across every caller's Monid client (clients
// themselves are cheap - see Service.Call). Ledger nil disables receipt
// recording.
type Config struct {
	HTTP              *http.Client
	Allowlist         monid.Allowlist
	Ledger            *fd.ReceiptsLedger
	MaxConcurrentRuns int
	CacheTTL          time.Duration
}

// Service runs Financial Datasets tools against Monid. It holds no
// per-caller state: the shared run cache and (optional) ledger are the
// only mutable state, and both are already safe for concurrent use.
type Service struct {
	http              *http.Client
	allowlist         monid.Allowlist
	ledger            *fd.ReceiptsLedger
	maxConcurrentRuns int
	cache             *RunCache
}

// New builds a Service from cfg. A nil cfg.HTTP or a MaxConcurrentRuns < 1
// falls back to monid.NewClient's own defaults (a fresh 180s-timeout
// client, 8 concurrent runs) at call time.
func New(cfg Config) *Service {
	return &Service{
		http:              cfg.HTTP,
		allowlist:         cfg.Allowlist,
		ledger:            cfg.Ledger,
		maxConcurrentRuns: cfg.MaxConcurrentRuns,
		cache:             NewRunCache(512),
	}
}

// Result is one tool outcome in Financial Datasets shape.
type Result struct {
	// Value is the bare FD value: a slice for list tools, an object (or
	// object-shaped map) for snapshot tools. Callers (MCP, REST) serialize
	// it as-is; MCP never wraps it further.
	Value any
	// WrapperKey is the REST envelope key ("income_statements", "prices",
	// ...); empty means Value is already the full, unwrapped response body
	// (snapshot-shaped tools, and the handful of list tools - screen_stocks,
	// get_interest_rates, get_index_fund - whose response mixes a list with
	// sibling scalar fields or never paginates).
	WrapperKey string
	// Paginate reports whether REST should apply cursor pagination
	// (fd.Paginate) over Value's slice: true for every tool whose Python
	// counterpart calls compat.paginate() (the statements, filings, prices,
	// news, earnings, financial_metrics, insider_trades,
	// segmented_financials, kpi_*, and institutional_holdings tools).
	// Value already carries the tool's full FD-limit-bounded record set;
	// REST slices it into pages, MCP returns it whole.
	Paginate bool
}

// callCtx is the per-Service.Call scratch context threaded through every
// handler: the request context, the caller-keyed Monid client, the owning
// Service (for cache/ledger access via call), and the tool name (for
// ledger receipts).
type callCtx struct {
	ctx    context.Context
	client *monid.Client
	svc    *Service
	tool   string
}

// Call runs one FD tool with the caller's own Monid API key: every Monid
// call it makes bills that caller's wallet. apiKey is used only to build a
// per-call *monid.Client (monid.NewClient) and is never logged or embedded
// in an error. Clients are cheap - the *http.Client connection pool is
// shared across every caller via Config.HTTP - so a fresh one is built per
// Call rather than cached per caller.
func (s *Service) Call(ctx context.Context, apiKey, tool string, args map[string]any) (Result, error) {
	handler, ok := toolHandlers[tool]
	if !ok {
		return Result{}, &providers.InputError{Msg: fmt.Sprintf("unknown tool %q", tool)}
	}
	if args == nil {
		args = map[string]any{}
	}
	return handler(s.newCallCtx(ctx, apiKey, tool), args)
}

// newCallCtx builds one callCtx for apiKey, mirroring the client
// construction Call itself does. Every exported capability that is not a
// Financial Datasets MCP tool (the coverage lists, get_all_financials,
// search_line_items - see go/service/coverage.go,
// go/service/allfinancials.go, go/service/searchlineitems.go) goes through
// this instead of the toolHandlers table, per this port's brief: register
// a capability in toolHandlers only when it maps to a real MCP tool name
// in go/mcpserver/tool_schemas.json.
func (s *Service) newCallCtx(ctx context.Context, apiKey, tool string) *callCtx {
	client := monid.NewClient(apiKey, s.http, s.allowlist, s.maxConcurrentRuns)
	return &callCtx{ctx: ctx, client: client, svc: s, tool: tool}
}

// run executes one Monid call, records a receipt, and serves repeat calls
// from the shared TTL cache: mirrors service.FinanceService._call. A cache
// hit performs no run, spends nothing, and writes no ledger row.
func (c *callCtx) run(provider, endpoint string, body, queryParams map[string]any) (*monid.Run, error) {
	key := newCacheKey(provider, endpoint, body, queryParams)
	if cached, ok := c.svc.cache.Get(key); ok {
		return cached, nil
	}
	run, err := c.client.Run(c.ctx, provider, endpoint, monid.Input{Body: body, QueryParams: queryParams})
	if err != nil {
		if c.svc.ledger != nil {
			_ = c.svc.ledger.RecordFailure(c.tool, provider, endpoint, err, body, queryParams)
		}
		return nil, err
	}
	if c.svc.ledger != nil {
		_ = c.svc.ledger.RecordSuccess(c.tool, run, body, queryParams)
	}
	c.svc.cache.Put(key, run, cacheTTLFor(endpoint))
	return run, nil
}

// unmarshalRun decodes one Monid run's output into a generic Go value.
func unmarshalRun(run *monid.Run) (any, error) {
	var value any
	if err := json.Unmarshal(run.Output, &value); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "provider payload is not valid JSON"}
	}
	return value, nil
}

// --- ordered JSON object (financial_metrics key reordering, segmented
// financials records: both build Financial Datasets responses whose key
// set is data-dependent, so a fixed Go struct can't express them; this
// mirrors building and re-keying a plain dict in the Python source) ---

// orderedJSONObject is a JSON object that serializes its keys in insertion
// order (Go's encoding/json sorts map[string]any keys alphabetically,
// which is wrong here).
type orderedJSONObject struct {
	keys   []string
	values map[string]any
}

func newOrderedJSONObject() *orderedJSONObject {
	return &orderedJSONObject{values: map[string]any{}}
}

func (o *orderedJSONObject) set(key string, value any) {
	if _, exists := o.values[key]; !exists {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *orderedJSONObject) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 0, 256)
	buf = append(buf, '{')
	for i, k := range o.keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := json.Marshal(o.values[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return buf, nil
}

// concurrent2 runs a and b concurrently and waits for both, mirroring the
// goroutine + sync.WaitGroup fan-out this port's brief calls for on every
// statements+filings join (income/balance/cash statements, financial
// metrics, per-ticker earnings): the Python source awaits these
// sequentially, but the two Monid calls are independent, so this port
// issues them in parallel instead. Each caller-supplied closure must write
// its own result into variables it closes over; concurrent2 only
// sequences the two goroutines.
func concurrent2(a, b func()) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a() }()
	go func() { defer wg.Done(); b() }()
	wg.Wait()
}

// --- get_company_facts ---

func (c *callCtx) getCompanyFacts(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	cikArg, err := argString(args, "cik")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required (cik lookup is not supported)"}
	}
	if cikArg != nil {
		return Result{}, &providers.InputError{Msg: "cik lookup is not supported; pass ticker instead"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	run, err := c.run(defillama, catalogEndpoint, nil, nil)
	if err != nil {
		return Result{}, err
	}
	name, found, err := findCompanyName(run.Output, symbol)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{Value: fd.NewErrorResponse("not_found", "No US company record exists for ticker "+symbol+".")}, nil
	}
	facts := fd.CompanyFacts{Ticker: &symbol}
	facts.Name = name
	return Result{Value: map[string]any{"company_facts": facts}}, nil
}

// --- get_income_statement / get_balance_sheet / get_cash_flow_statement ---

// statementResponseKeys mirrors service.py's STATEMENT_RESPONSE_KEYS: the
// REST wrapper key for each statement kind.
var statementResponseKeys = map[string]string{
	"income":  "income_statements",
	"balance": "balance_sheets",
	"cash":    "cash_flow_statements",
}

// ttmFlowIncome/ttmMeanIncome/ttmFlowCash mirror service.py's
// _TTM_FLOW_INCOME/_TTM_MEAN_INCOME/_TTM_FLOW_CASH. Balance sheet TTM rows
// use no flow/mean labels at all: every balance field is point-in-time
// (carried from the final quarter), matching an empty LabelSet for both.
var ttmFlowIncome = providers.NewLabelSet(
	"Revenue", "Cost of Revenue", "Gross Profit", "Operating Expenses",
	"Operating Income", "EBIT", "Income Tax", "Net Income", "Net Income to Common",
	"EPS (Basic)", "EPS (Diluted)", "Non-Operating Items|Non-Operating Interest Expense",
)
var ttmMeanIncome = providers.NewLabelSet("Shares Outstanding (Basic)", "Shares Outstanding (Diluted)")
var ttmFlowCash = providers.NewLabelSet(
	"Net Income", "Depreciation and Amortization", "Cash Flow from Operating Activities",
	"Cash Flow from Investing Activities", "Cash Flow from Financing Activities",
	"Net Cash Flow", "Free Cash Flow", "Cash Flow from Investing Activities|Capital Expenditure",
	"Cash Flow from Financing Activities|Common Dividends",
)
var emptyLabelSet = providers.NewLabelSet()

// statementArgs is the parsed, pre-validated argument set shared by the
// three statement tools and get_financial_metrics, mirroring their
// identical parameter lists.
type statementArgs struct {
	ticker string
	period string
	limit  int
	report dateFilters
	filing dateFilters
}

// parseStatementArgs validates ticker/cik/period/limit and the two
// report_period*/filing_date* filter families, mirroring the shared
// validation prologue of _statement_response and
// _financial_metrics_response.
func parseStatementArgs(args map[string]any, defaultPeriod string, defaultLimit, maxLimit int) (statementArgs, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return statementArgs{}, err
	}
	cikArg, err := argString(args, "cik")
	if err != nil {
		return statementArgs{}, err
	}
	if tickerArg == nil {
		return statementArgs{}, &providers.InputError{Msg: "ticker is required (cik lookup is not supported)"}
	}
	if cikArg != nil {
		return statementArgs{}, &providers.InputError{Msg: "cik lookup is not supported; pass ticker instead"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return statementArgs{}, err
	}
	periodRaw, err := argStringDefault(args, "period", defaultPeriod)
	if err != nil {
		return statementArgs{}, err
	}
	period, err := validatePeriod(periodRaw)
	if err != nil {
		return statementArgs{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", defaultLimit)
	if err != nil {
		return statementArgs{}, err
	}
	limit, err := validateLimit(limitRaw, maxLimit)
	if err != nil {
		return statementArgs{}, err
	}
	report, err := parseDateFilterGroup(args, "report_period")
	if err != nil {
		return statementArgs{}, err
	}
	filing, err := parseDateFilterGroup(args, "filing_date")
	if err != nil {
		return statementArgs{}, err
	}
	return statementArgs{ticker: symbol, period: period, limit: limit, report: report, filing: filing}, nil
}

// parseDateFilterGroup reads the five prefix/{prefix}_gte/{prefix}_lte/
// {prefix}_gt/{prefix}_lt string args and validates them as one
// dateFilters, mirroring service._date_filters called from each tool's
// own argument set.
func parseDateFilterGroup(args map[string]any, prefix string) (dateFilters, error) {
	exact, err := argString(args, prefix)
	if err != nil {
		return dateFilters{}, err
	}
	gte, err := argString(args, prefix+"_gte")
	if err != nil {
		return dateFilters{}, err
	}
	lte, err := argString(args, prefix+"_lte")
	if err != nil {
		return dateFilters{}, err
	}
	gt, err := argString(args, prefix+"_gt")
	if err != nil {
		return dateFilters{}, err
	}
	lt, err := argString(args, prefix+"_lt")
	if err != nil {
		return dateFilters{}, err
	}
	return buildDateFilters(exact, gte, lte, gt, lt, prefix)
}

// checkAsReported mirrors server.py's as_reported honest rejection for the
// three statement tools: as_reported=true is a bad_request at zero cost.
func checkAsReported(args map[string]any) error {
	asReported, err := argBoolDefault(args, "as_reported", false)
	if err != nil {
		return err
	}
	if asReported {
		return &providers.InputError{Msg: "as_reported is not supported by the Monid-backed server; " +
			"as_reported=True cannot be answered honestly."}
	}
	return nil
}

func (c *callCtx) getIncomeStatement(args map[string]any) (Result, error) {
	if err := checkAsReported(args); err != nil {
		return Result{}, err
	}
	return c.statementResponse("income", args)
}

func (c *callCtx) getBalanceSheet(args map[string]any) (Result, error) {
	if err := checkAsReported(args); err != nil {
		return Result{}, err
	}
	return c.statementResponse("balance", args)
}

// getCashFlowStatement sources this statement from marketbeat, not from
// the normalized feed the income statement and balance sheet use. That
// feed's cash flow subtotals disagree with SEC on three of four lines
// (see marketbeatcashflow.go for the measurements).
//
// ttm is still composed from the normalized feed's quarters: marketbeat
// reports filed periods only, and a trailing-twelve-month window is not
// one. Those rows carry the caveat the whole TTM path already carries.
func (c *callCtx) getCashFlowStatement(args map[string]any) (Result, error) {
	if err := checkAsReported(args); err != nil {
		return Result{}, err
	}
	parsed, err := parseStatementArgs(args, "ttm", 4, 100)
	if err != nil {
		return Result{}, err
	}
	if parsed.period == "ttm" {
		return c.statementResponse("cash", args)
	}
	return c.marketbeatCashFlowResponse(parsed)
}

// statementResponse mirrors service._statement_response. The FD JSON
// Schema default period is "ttm" (docs/fd-mcp-tool-schemas.json), limit 4,
// max 100 - this port's brief calls this out explicitly since Python's own
// internal default (period="annual") differs from the published schema.
func (c *callCtx) statementResponse(statement string, args map[string]any) (Result, error) {
	parsed, err := parseStatementArgs(args, "ttm", 4, 100)
	if err != nil {
		return Result{}, err
	}

	var statementsRun *monid.Run
	var statementsErr error
	var filingsRun *monid.Run
	var filingsErr error
	concurrent2(
		func() {
			statementsRun, statementsErr = c.run(defillama, statementsEndpoint, nil,
				map[string]any{"ticker": parsed.ticker, "country": "US"})
		},
		func() {
			filingsRun, filingsErr = c.run(defillama, filingsEndpoint, nil,
				map[string]any{"ticker": parsed.ticker, "country": "US"})
		},
	)
	if statementsErr != nil {
		return Result{}, statementsErr
	}

	value, err := unmarshalRun(statementsRun)
	if err != nil {
		return Result{}, err
	}
	identityMap, err := buildFilingIdentityMap(filingsRun, filingsErr, parsed.ticker, parsed.period != "quarterly")
	if err != nil {
		return Result{}, err
	}
	records, err := buildStatementRecords(statement, parsed, value, identityMap)
	if err != nil {
		return Result{}, err
	}
	return Result{Value: records, WrapperKey: statementResponseKeys[statement], Paginate: true}, nil
}

// buildStatementRecords derives one statement kind's FD records
// (income/balance/cash) from an already-fetched, already-unmarshaled
// statements payload and an already-joined filing identity map: this is
// the row-filtering, sorting, limiting, and per-row FD record construction
// that used to live inline in statementResponse. Factored out so
// getAllFinancials can build all three statement kinds from ONE
// concurrently-fetched statements+filings pair instead of paying for the
// same /equities/v1/statements call three times (see getAllFinancials's
// own doc comment for why).
func buildStatementRecords(statement string, parsed statementArgs, value any, identityMap map[string]FilingIdentity) ([]any, error) {
	series, err := providers.ParseStatementSeries(value, statement)
	if err != nil {
		return nil, err
	}
	rows := statementRowsForPeriod(series, parsed.period, statement)
	endMonth := providers.FiscalYearEndMonth(series)

	if identityMap == nil && parsed.filing.any() {
		return nil, &monid.RunError{Kind: monid.ErrProviderHTTP,
			Message: "Filing identity join failed; filing_date filters cannot be applied."}
	}
	rows = applyFilingFilters(rows, identityMap, parsed.filing)
	rows = filterPeriodRows(rows, parsed.report)
	sortPeriodRowsDesc(rows)
	if len(rows) > parsed.limit {
		rows = rows[:parsed.limit]
	}

	records := make([]any, 0, len(rows))
	for _, row := range rows {
		key := row.ReportPeriod.Format(dateLayout)
		identity := lookupFilingIdentity(identityMap, row.ReportPeriod)
		// Prefer the filing's own period end over the statements feed's
		// month-end rounding: for Apple FY2025 that is 2025-09-27 rather
		// than 2025-09-30, which is what the 10-K actually reports.
		if identity != nil && identity.ReportDate != nil {
			key = identity.ReportDate.Format(dateLayout)
		}
		var fiscalPeriod *string
		if parsed.period != "ttm" {
			fiscalPeriod = providers.FiscalPeriodLabel(row, endMonth, parsed.period == "annual")
		}
		switch statement {
		case "income":
			records = append(records, buildIncomeStatement(parsed.ticker, parsed.period, key, fiscalPeriod, row.Values, identity))
		case "balance":
			records = append(records, buildBalanceSheet(parsed.ticker, parsed.period, key, fiscalPeriod, row.Values, identity))
		case "cash":
			records = append(records, buildCashFlowStatement(parsed.ticker, parsed.period, key, fiscalPeriod, row.Values, identity))
		}
	}
	return records, nil
}

// statementRowsForPeriod mirrors service._statement_rows.
func statementRowsForPeriod(series providers.StatementSeries, period, statement string) []providers.PeriodRow {
	switch period {
	case "annual":
		return append([]providers.PeriodRow{}, series.Annual...)
	case "quarterly":
		return append([]providers.PeriodRow{}, series.Quarterly...)
	default: // ttm
		flow, mean := emptyLabelSet, emptyLabelSet
		switch statement {
		case "income":
			flow, mean = ttmFlowIncome, ttmMeanIncome
		case "cash":
			flow = ttmFlowCash
		}
		return providers.DeriveTTMRows(series.Quarterly, flow, mean)
	}
}

func filterPeriodRows(rows []providers.PeriodRow, filters dateFilters) []providers.PeriodRow {
	if !filters.any() {
		return rows
	}
	out := make([]providers.PeriodRow, 0, len(rows))
	for _, row := range rows {
		if filters.matches(row.ReportPeriod) {
			out = append(out, row)
		}
	}
	return out
}

func sortPeriodRowsDesc(rows []providers.PeriodRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ReportPeriod.After(rows[j].ReportPeriod) })
}

// applyFilingFilters mirrors service._apply_filing_filters: rows whose
// joined filing_date does not satisfy the filing_date* filters drop; when
// the identity join is unavailable (nil map) and filters are active every
// row drops (the caller has already turned that combination into an
// upstream_error before rows ever get this far, in statementResponse).
func applyFilingFilters(rows []providers.PeriodRow, identityMap map[string]FilingIdentity, filing dateFilters) []providers.PeriodRow {
	if !filing.any() {
		return rows
	}
	if identityMap == nil {
		return nil
	}
	out := make([]providers.PeriodRow, 0, len(rows))
	for _, row := range rows {
		// Same tolerant join the record builder uses: an exact-date
		// lookup here would drop every annual row, since the statements
		// feed and the filings feed disagree on the period end.
		identity := lookupFilingIdentity(identityMap, row.ReportPeriod)
		if identity == nil || identity.FilingDate == nil {
			continue
		}
		if filing.matches(*identity.FilingDate) {
			out = append(out, row)
		}
	}
	return out
}

// buildFilingIdentityMap joins DefiLlama filings onto statement/metrics
// report periods, mirroring service._filing_identity_map. filingsErr
// being non-nil (the filings Monid call itself failed) is swallowed into
// a nil map with no error - matching Python's `except UpstreamError:
// return None`. A parse failure of a successful run's payload
// (providers.NormalizeFilings returning an error) propagates, matching
// Python's normalize_filings call sitting outside that try/except.
func buildFilingIdentityMap(filingsRun *monid.Run, filingsErr error, ticker string, annual bool) (map[string]FilingIdentity, error) {
	if filingsErr != nil {
		return nil, nil
	}
	filings, err := providers.NormalizeFilings(filingsRun.Output, ticker, nil, 10_000, nil, nil)
	if err != nil {
		return nil, err
	}
	forms := quarterlyForms
	if annual {
		forms = annualForms
	}
	type candidate struct {
		filingDate string
		identity   FilingIdentity
	}
	best := map[string]candidate{}
	for _, f := range filings {
		if f.FilingType == nil || !forms[strings.ToUpper(*f.FilingType)] {
			continue
		}
		if f.ReportDate == nil || f.FilingDate == nil || f.URL == nil {
			continue
		}
		reportDay, filingDay := *f.ReportDate, *f.FilingDate
		cur, exists := best[reportDay]
		if exists && cur.filingDate >= filingDay {
			continue
		}
		form := strings.ToUpper(*f.FilingType)
		filingTime, timeErr := time.Parse(dateLayout, filingDay)
		identity := FilingIdentity{AccessionNumber: f.AccessionNumber, FormType: &form, FilingURL: f.URL}
		if timeErr == nil {
			identity.FilingDate = &filingTime
		}
		if reportTime, rerr := time.Parse(dateLayout, reportDay); rerr == nil {
			identity.ReportDate = &reportTime
		}
		best[reportDay] = candidate{filingDate: filingDay, identity: identity}
	}
	out := make(map[string]FilingIdentity, len(best))
	for day, c := range best {
		out[day] = c.identity
	}
	return out, nil
}

// filingJoinToleranceDays bounds how far a statement period may sit from a
// filing's reported period end and still be considered the same period.
//
// The two feeds disagree by construction: the statements feed rounds to
// month end while filings carry the real fiscal close, which for a 52/53
// week fiscal year can land up to six days either side. Quarters are ~90
// days apart, so a window this size cannot reach an adjacent period.
const filingJoinToleranceDays = 10

// lookupFilingIdentity finds the filing covering a statement period.
//
// It tries an exact date match first, then falls back to the nearest
// filing within filingJoinToleranceDays. The exact match alone silently
// failed for every annual statement - the statements feed said
// 2025-09-30 while the filing said 2025-09-27 - which dropped
// accession_number, form_type, filing_url and filing_date from every
// annual record. Nothing errored; the fields were simply absent.
func lookupFilingIdentity(identityMap map[string]FilingIdentity, period time.Time) *FilingIdentity {
	if identityMap == nil {
		return nil
	}
	if id, ok := identityMap[period.Format(dateLayout)]; ok {
		return &id
	}
	var best *FilingIdentity
	bestDelta := time.Duration(filingJoinToleranceDays+1) * 24 * time.Hour
	for key := range identityMap {
		day, err := time.Parse(dateLayout, key)
		if err != nil {
			continue
		}
		delta := day.Sub(period)
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			id := identityMap[key]
			bestDelta, best = delta, &id
		}
	}
	if bestDelta > time.Duration(filingJoinToleranceDays)*24*time.Hour {
		return nil
	}
	return best
}

func setIdentity(accession, formType, filingURL, filingDate **string, identity *FilingIdentity) {
	if identity == nil {
		return
	}
	*accession = identity.AccessionNumber
	*formType = identity.FormType
	*filingURL = identity.FilingURL
	if identity.FilingDate != nil {
		s := identity.FilingDate.Format(dateLayout)
		*filingDate = &s
	}
}

func numFromValues(values map[string]any, label string) *float64 {
	v, ok := values[label]
	if !ok {
		return nil
	}
	if _, isBool := v.(bool); isBool {
		return nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	return &f
}

// buildIncomeStatement mirrors fd.income_statement_record + fd.py's
// _INCOME_FIELDS mapping.
func buildIncomeStatement(ticker, period, reportPeriod string, fiscalPeriod *string, values map[string]any, identity *FilingIdentity) fd.IncomeStatement {
	rec := fd.IncomeStatement{Ticker: &ticker, ReportPeriod: &reportPeriod, FiscalPeriod: fiscalPeriod, Period: &period}
	setIdentity(&rec.AccessionNumber, &rec.FormType, &rec.FilingURL, &rec.FilingDate, identity)
	rec.Revenue = numFromValues(values, "Revenue")
	rec.CostOfRevenue = numFromValues(values, "Cost of Revenue")
	rec.GrossProfit = numFromValues(values, "Gross Profit")
	rec.OperatingExpense = numFromValues(values, "Operating Expenses")
	rec.SellingGeneralAndAdministrativeExpenses = numFromValues(values, "Operating Expenses|Selling, General & Administrative")
	rec.ResearchAndDevelopment = numFromValues(values, "Operating Expenses|Research and Development")
	rec.OperatingIncome = numFromValues(values, "Operating Income")
	rec.InterestExpense = numFromValues(values, "Non-Operating Items|Non-Operating Interest Expense")
	rec.EBIT = numFromValues(values, "EBIT")
	rec.IncomeTaxExpense = numFromValues(values, "Income Tax")
	rec.NetIncome = numFromValues(values, "Net Income")
	rec.NetIncomeCommonStock = numFromValues(values, "Net Income to Common")
	rec.EarningsPerShare = numFromValues(values, "EPS (Basic)")
	rec.EarningsPerShareDiluted = numFromValues(values, "EPS (Diluted)")
	rec.WeightedAverageShares = numFromValues(values, "Shares Outstanding (Basic)")
	rec.WeightedAverageSharesDiluted = numFromValues(values, "Shares Outstanding (Diluted)")
	return rec
}

// buildBalanceSheet mirrors fd.balance_sheet_record + fd.py's
// _BALANCE_FIELDS mapping.
func buildBalanceSheet(ticker, period, reportPeriod string, fiscalPeriod *string, values map[string]any, identity *FilingIdentity) fd.BalanceSheet {
	rec := fd.BalanceSheet{Ticker: &ticker, ReportPeriod: &reportPeriod, FiscalPeriod: fiscalPeriod, Period: &period}
	setIdentity(&rec.AccessionNumber, &rec.FormType, &rec.FilingURL, &rec.FilingDate, identity)
	rec.TotalAssets = numFromValues(values, "Total Assets")
	rec.CurrentAssets = numFromValues(values, "Total Current Assets")
	rec.CashAndEquivalents = numFromValues(values, "Total Current Assets|Cash and Cash Equivalents")
	rec.Inventory = numFromValues(values, "Total Current Assets|Inventory")
	rec.TradeAndNonTradeReceivables = numFromValues(values, "Total Current Assets|Accounts Receivable")
	rec.NonCurrentAssets = numFromValues(values, "Total Non-Current Assets")
	rec.PropertyPlantAndEquipment = numFromValues(values, "Total Non-Current Assets|Property, Plant & Equipment")
	rec.GoodwillAndIntangibleAssets = numFromValues(values, "Total Non-Current Assets|Goodwill and Intangible Assets")
	rec.OutstandingShares = numFromValues(values, "Shares Outstanding (Basic)")
	rec.TotalLiabilities = numFromValues(values, "Total Liabilities")
	rec.CurrentLiabilities = numFromValues(values, "Total Current Liabilities")
	rec.CurrentDebt = numFromValues(values, "Total Current Liabilities|Short-Term Debt")
	rec.NonCurrentLiabilities = numFromValues(values, "Total Non-Current Liabilities")
	rec.NonCurrentDebt = numFromValues(values, "Total Non-Current Liabilities|Long-Term Debt")
	rec.ShareholdersEquity = numFromValues(values, "Total Shareholders Equity")
	rec.RetainedEarnings = numFromValues(values, "Total Shareholders Equity|Retained Earnings")
	rec.TotalDebt = numFromValues(values, "Debt")
	return rec
}

// buildCashFlowStatement mirrors fd.cash_flow_record + fd.py's
// _CASH_FIELDS mapping.
func buildCashFlowStatement(ticker, period, reportPeriod string, fiscalPeriod *string, values map[string]any, identity *FilingIdentity) fd.CashFlowStatement {
	rec := fd.CashFlowStatement{Ticker: &ticker, ReportPeriod: &reportPeriod, FiscalPeriod: fiscalPeriod, Period: &period}
	setIdentity(&rec.AccessionNumber, &rec.FormType, &rec.FilingURL, &rec.FilingDate, identity)
	rec.NetIncome = numFromValues(values, "Net Income")
	rec.DepreciationAndAmortization = numFromValues(values, "Depreciation and Amortization")
	rec.ShareBasedCompensation = numFromValues(values, "Cash Flow from Operating Activities|Share-Based Compensation")
	rec.NetCashFlowFromOperations = numFromValues(values, "Cash Flow from Operating Activities")
	rec.CapitalExpenditure = numFromValues(values, "Cash Flow from Investing Activities|Capital Expenditure")
	rec.NetCashFlowFromInvesting = numFromValues(values, "Cash Flow from Investing Activities")
	rec.DividendsAndOtherCashDistributions = numFromValues(values, "Cash Flow from Financing Activities|Common Dividends")
	rec.NetCashFlowFromFinancing = numFromValues(values, "Cash Flow from Financing Activities")
	rec.ChangeInCashAndEquivalents = numFromValues(values, "Net Cash Flow")
	rec.EndingCashBalance = numFromValues(values, "End Cash Position")
	rec.FreeCashFlow = numFromValues(values, "Free Cash Flow")
	return rec
}

// --- get_financial_metrics ---

// metricsKeyOrder mirrors service.py's _METRICS_KEY_ORDER exactly.
var metricsKeyOrder = []string{
	"ticker",
	"report_period",
	"fiscal_period",
	"period",
	"currency",
	"accession_number",
	"form_type",
	"filing_url",
	"filing_date",
	"filing_datetime",
	"enterprise_value",
	"price_to_earnings_ratio",
	"price_to_book_ratio",
	"price_to_sales_ratio",
	"enterprise_value_to_ebitda_ratio",
	"enterprise_value_to_revenue_ratio",
	"free_cash_flow_yield",
	"peg_ratio",
	"gross_margin",
	"operating_margin",
	"net_margin",
	"return_on_equity",
	"return_on_assets",
	"return_on_invested_capital",
	"asset_turnover",
	"inventory_turnover",
	"receivables_turnover",
	"days_sales_outstanding",
	"operating_cycle",
	"working_capital_turnover",
	"current_ratio",
	"quick_ratio",
	"cash_ratio",
	"operating_cash_flow_ratio",
	"debt_to_equity",
	"debt_to_assets",
	"interest_coverage",
	"revenue_growth",
	"earnings_growth",
	"book_value_growth",
	"earnings_per_share_growth",
	"free_cash_flow_growth",
	"operating_income_growth",
	"ebitda_growth",
	"payout_ratio",
	"earnings_per_share",
	"book_value_per_share",
	"free_cash_flow_per_share",
}

func (c *callCtx) getFinancialMetrics(args map[string]any) (Result, error) {
	parsed, err := parseStatementArgs(args, "ttm", 4, 100)
	if err != nil {
		return Result{}, err
	}

	var statementsRun *monid.Run
	var statementsErr error
	var filingsRun *monid.Run
	var filingsErr error
	concurrent2(
		func() {
			statementsRun, statementsErr = c.run(defillama, statementsEndpoint, nil,
				map[string]any{"ticker": parsed.ticker, "country": "US"})
		},
		func() {
			filingsRun, filingsErr = c.run(defillama, filingsEndpoint, nil,
				map[string]any{"ticker": parsed.ticker, "country": "US"})
		},
	)
	if statementsErr != nil {
		return Result{}, statementsErr
	}
	value, err := unmarshalRun(statementsRun)
	if err != nil {
		return Result{}, err
	}

	metricsFilters := providers.MetricsFilters{Exact: parsed.report.Exact, GTE: parsed.report.GTE, LTE: parsed.report.LTE, GT: parsed.report.GT, LT: parsed.report.LT}
	data, err := providers.NormalizeFinancialMetrics(value, parsed.ticker, providers.MetricsPeriod(parsed.period), parsed.limit, metricsFilters)
	if err != nil {
		return Result{}, err
	}

	identityMap, err := buildFilingIdentityMap(filingsRun, filingsErr, parsed.ticker, parsed.period != "quarterly")
	if err != nil {
		return Result{}, err
	}
	if identityMap == nil && parsed.filing.any() {
		return Result{}, &monid.RunError{Kind: monid.ErrProviderHTTP,
			Message: "Filing identity join failed; filing_date filters cannot be applied."}
	}

	records := make([]any, 0, len(data.Records))
	for _, record := range data.Records {
		raw, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return Result{}, marshalErr
		}
		var base map[string]any
		if unmarshalErr := json.Unmarshal(raw, &base); unmarshalErr != nil {
			return Result{}, unmarshalErr
		}
		reportDay, hasReportDay := base["report_period"].(string)
		if identityMap != nil && hasReportDay {
			if identity, ok := identityMap[reportDay]; ok {
				if identity.AccessionNumber != nil {
					base["accession_number"] = *identity.AccessionNumber
				}
				if identity.FormType != nil {
					base["form_type"] = *identity.FormType
				}
				if identity.FilingURL != nil {
					base["filing_url"] = *identity.FilingURL
				}
				if identity.FilingDate != nil {
					base["filing_date"] = identity.FilingDate.Format(dateLayout)
				}
			}
		}
		if parsed.filing.any() {
			filingDayStr, _ := base["filing_date"].(string)
			var filingDay *time.Time
			if filingDayStr != "" {
				if t, parseErr := time.Parse(dateLayout, filingDayStr); parseErr == nil {
					filingDay = &t
				}
			}
			if filingDay == nil || !parsed.filing.matches(*filingDay) {
				continue
			}
		}
		if parsed.period == "ttm" {
			delete(base, "accession_number")
			delete(base, "form_type")
			delete(base, "filing_url")
			delete(base, "filing_date")
		}
		records = append(records, orderMetricsRecord(base))
	}
	return Result{Value: records, WrapperKey: "financial_metrics", Paginate: true}, nil
}

// orderMetricsRecord mirrors service._ordered_metrics_record.
func orderMetricsRecord(record map[string]any) *orderedJSONObject {
	ordered := newOrderedJSONObject()
	seen := make(map[string]bool, len(record))
	for _, key := range metricsKeyOrder {
		if value, ok := record[key]; ok {
			ordered.set(key, value)
			seen[key] = true
		}
	}
	for key, value := range record {
		if !seen[key] {
			ordered.set(key, value)
		}
	}
	return ordered
}

// --- get_financial_metrics_snapshot ---

func (c *callCtx) getFinancialMetricsSnapshot(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required (cik lookup is not supported)"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	run, err := c.run(defillama, summaryEndpoint, nil, map[string]any{"ticker": symbol, "country": "US"})
	if err != nil {
		return Result{}, err
	}
	summary, err := providers.ParseSummary(run.Output)
	if err != nil {
		return Result{}, err
	}
	snapshot := providers.BuildFinancialMetricSnapshot(symbol, summary)
	return Result{Value: map[string]any{"snapshot": snapshot}}, nil
}

// --- get_filings ---

func (c *callCtx) getFilings(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	cikArg, err := argString(args, "cik")
	if err != nil {
		return Result{}, err
	}
	if cikArg != nil {
		return Result{}, &providers.InputError{Msg: "cik lookup is not supported; pass ticker instead"}
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker or cik is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	filingTypeArg, err := argStringSlice(args, "filing_type")
	if err != nil {
		return Result{}, err
	}
	forms, err := validateFilingTypes(filingTypeArg)
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 100)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 1000)
	if err != nil {
		return Result{}, err
	}
	run, err := c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": symbol, "country": "US"})
	if err != nil {
		return Result{}, err
	}
	filings, err := providers.NormalizeFilings(run.Output, symbol, forms, limit, nil, nil)
	if err != nil {
		return Result{}, err
	}
	records := make([]any, len(filings))
	for i, f := range filings {
		records[i] = f
	}
	return Result{Value: records, WrapperKey: "filings", Paginate: true}, nil
}

// --- get_stock_prices / get_stock_price ---

func (c *callCtx) getStockPrices(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	intervalRaw, err := argStringDefault(args, "interval", "day")
	if err != nil {
		return Result{}, err
	}
	interval, err := validateInterval(intervalRaw)
	if err != nil {
		return Result{}, err
	}
	multiplier, err := argIntDefault(args, "interval_multiplier", 1)
	if err != nil {
		return Result{}, err
	}
	if multiplier != 1 {
		return Result{}, &providers.InputError{Msg: "only interval_multiplier=1 is supported by the Monid-backed server."}
	}
	startArg, err := argString(args, "start_date")
	if err != nil {
		return Result{}, err
	}
	endArg, err := argString(args, "end_date")
	if err != nil {
		return Result{}, err
	}
	start, end, err := validateDateRange(startArg, endArg, "start_date", "end_date")
	if err != nil {
		return Result{}, err
	}
	if start == nil || end == nil {
		return Result{}, &providers.InputError{Msg: "start_date and end_date are required"}
	}
	run, err := c.run(defillama, ohlcvEndpoint, nil, map[string]any{"ticker": symbol, "country": "US", "timeframe": "MAX"})
	if err != nil {
		return Result{}, err
	}
	prices, err := providers.NormalizePrices(run.Output, start.Format(dateLayout), end.Format(dateLayout), interval)
	if err != nil {
		return Result{}, err
	}
	records := make([]any, len(prices))
	for i, p := range prices {
		records[i] = p
	}
	return Result{Value: records, WrapperKey: "prices", Paginate: true}, nil
}

func (c *callCtx) getStockPrice(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	run, err := c.run(defillama, summaryEndpoint, nil, map[string]any{"ticker": symbol, "country": "US"})
	if err != nil {
		return Result{}, err
	}
	summary, err := providers.ParseSummary(run.Output)
	if err != nil {
		return Result{}, err
	}
	if summary.Price == nil {
		return Result{}, &providers.SchemaDriftError{Msg: "DefiLlama summary omitted a finite numeric current price"}
	}
	snapshot, err := providers.BuildPriceSnapshot(symbol, summary)
	if err != nil {
		return Result{}, err
	}
	return Result{Value: map[string]any{"snapshot": snapshot}}, nil
}

// --- get_news ---

func (c *callCtx) getNews(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "market-wide news is not routed; pass ticker"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 5)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 10)
	if err != nil {
		return Result{}, err
	}
	body := map[string]any{
		"searchBy": map[string]any{
			"type":   "entity",
			"entity": map[string]any{"type": "ticker", "ticker": symbol},
		},
		"sortBy": map[string]any{"type": "newest"},
		"limit":  limit,
	}
	run, err := c.run(contextDev, newsEndpoint, body, nil)
	if err != nil {
		return Result{}, err
	}
	articles, err := providers.NormalizeNews(run.Output, symbol, limit)
	if err != nil {
		return Result{}, err
	}
	records := make([]any, len(articles))
	for i, a := range articles {
		records[i] = a
	}
	return Result{Value: records, WrapperKey: "news", Paginate: true}, nil
}

// --- get_earnings ---

// earningsFeedLimit mirrors server.py's hardcoded limit=5 for the
// market-wide earnings feed: get_earnings' FD JSON Schema exposes no limit
// parameter at all (docs/fd-mcp-tools.json's get_earnings params are just
// ["ticker"]), and the Python MCP surface always requests 5 regardless of
// path (ticker or market-wide feed).
const earningsFeedLimit = 5

func (c *callCtx) getEarnings(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	var records []fd.EarningsRecord
	if tickerArg == nil {
		records, err = c.earningsFeed(earningsFeedLimit)
	} else {
		var symbol string
		symbol, err = validateTicker(*tickerArg)
		if err == nil {
			records, err = c.earningsForTicker(symbol, earningsFeedLimit)
		}
	}
	if err != nil {
		return Result{}, err
	}
	out := make([]any, len(records))
	for i, r := range records {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "earnings", Paginate: true}, nil
}

// earningsForTicker composes earnings records for one ticker from
// statements + filings, mirroring service._earnings_for_ticker. Per this
// port's brief, the two independent Monid calls run concurrently instead
// of the Python source's sequential await/await (measured 2.1x faster).
func (c *callCtx) earningsForTicker(ticker string, limit int) ([]fd.EarningsRecord, error) {
	var statementsRun *monid.Run
	var statementsErr error
	var filingsRun *monid.Run
	var filingsErr error
	concurrent2(
		func() {
			statementsRun, statementsErr = c.run(defillama, statementsEndpoint, nil,
				map[string]any{"ticker": ticker, "country": "US"})
		},
		func() {
			filingsRun, filingsErr = c.run(defillama, filingsEndpoint, nil,
				map[string]any{"ticker": ticker, "country": "US"})
		},
	)
	if statementsErr != nil {
		return nil, statementsErr
	}
	if filingsErr != nil {
		return nil, filingsErr
	}
	statementsValue, err := unmarshalRun(statementsRun)
	if err != nil {
		return nil, err
	}
	filings, err := providers.NormalizeFilings(filingsRun.Output, ticker, nil, 10_000, nil, nil)
	if err != nil {
		return nil, err
	}
	data, err := providers.NormalizeEarnings(statementsValue, filingsToRaw(filings), ticker, limit)
	if err != nil {
		return nil, err
	}
	return data.Records, nil
}

func filingsToRaw(filings []fd.Filing) []providers.RawFiling {
	out := make([]providers.RawFiling, 0, len(filings))
	for _, f := range filings {
		if f.FilingDate == nil || f.ReportDate == nil || f.FilingType == nil || f.URL == nil {
			continue
		}
		out = append(out, providers.RawFiling{
			FilingDate: *f.FilingDate, ReportDate: *f.ReportDate, Form: *f.FilingType, PrimaryDocumentURL: *f.URL,
		})
	}
	return out
}

// earningsFeed composes the market-wide earnings feed from the Nasdaq
// calendar, mirroring service._earnings_feed. Per this port's brief, the
// per-reporter earningsForTicker calls fan out over goroutines instead of
// the Python source's sequential loop.
func (c *callCtx) earningsFeed(limit int) ([]fd.EarningsRecord, error) {
	calendarRun, err := c.run(nasdaq, earningsCalendarEndpoint, nil, map[string]any{"limit": limit})
	if err != nil {
		return nil, err
	}
	value, err := unmarshalRun(calendarRun)
	if err != nil {
		return nil, err
	}
	reporters, err := parseEarningsCalendar(value, limit)
	if err != nil {
		return nil, err
	}
	// entry_symbol = validate_ticker(reporter.ticker) runs unguarded in
	// Python (outside the try/except around the per-ticker composition),
	// so one malformed calendar ticker fails the whole feed request - this
	// pre-validation pass mirrors that before any fan-out starts.
	symbols := make([]string, len(reporters))
	for i, reporter := range reporters {
		symbol, verr := validateTicker(reporter.Ticker)
		if verr != nil {
			return nil, verr
		}
		symbols[i] = symbol
	}

	batches := make([][]fd.EarningsRecord, len(symbols))
	var wg sync.WaitGroup
	wg.Add(len(symbols))
	for i, symbol := range symbols {
		go func(i int, symbol string) {
			defer wg.Done()
			composed, cerr := c.earningsForTicker(symbol, 1)
			if cerr != nil {
				// mirrors `except (UpstreamError, SchemaDriftError): continue` -
				// earningsForTicker can only fail with a *monid.RunError or a
				// *providers.SchemaDriftError, both swallowed here.
				return
			}
			batches[i] = composed
		}(i, symbol)
	}
	wg.Wait()

	var records []fd.EarningsRecord
	for _, batch := range batches {
		records = append(records, batch...)
	}
	sort.SliceStable(records, func(i, j int) bool { return earningsRecordNewer(records[i], records[j]) })
	return records, nil
}

// earningsRecordNewer mirrors service._earnings_filing_sort_key under
// reverse=True: records with a filing_date sort before those without one;
// among dated records, the newer filing_date sorts first.
func earningsRecordNewer(a, b fd.EarningsRecord) bool {
	aHas, bHas := a.FilingDate != nil, b.FilingDate != nil
	if aHas != bHas {
		return aHas
	}
	if !aHas {
		return false
	}
	return *a.FilingDate > *b.FilingDate
}

// earningsCalendarEntry mirrors earnings.EarningsCalendarEntry.
type earningsCalendarEntry struct {
	Ticker     string
	ReportDate *time.Time
	FilingDate *time.Time
}

// calendarRows mirrors earnings._calendar_rows.
func calendarRows(value any) ([]any, error) {
	current := value
	for i := 0; i < 5; i++ {
		if arr, ok := current.([]any); ok {
			return arr, nil
		}
		obj, ok := current.(map[string]any)
		if !ok {
			break
		}
		var child any
		for _, key := range []string{"rows", "results", "data", "earnings", "calendar"} {
			if c, exists := obj[key]; exists {
				switch c.(type) {
				case []any, map[string]any:
					child = c
				}
			}
			if child != nil {
				break
			}
		}
		if child == nil {
			break
		}
		current = child
	}
	return nil, &providers.SchemaDriftError{Msg: "Nasdaq earnings calendar payload is not parseable"}
}

// parseEarningsCalendar mirrors earnings.parse_earnings_calendar: unique
// reporters ordered most-recent first by filing date then report date.
func parseEarningsCalendar(value any, limit int) ([]earningsCalendarEntry, error) {
	rows, err := calendarRows(value)
	if err != nil {
		return nil, err
	}
	entries := make([]earningsCalendarEntry, 0, len(rows))
	for index, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		if !ok {
			return nil, &providers.SchemaDriftError{Msg: fmt.Sprintf("Nasdaq earnings calendar row[%d] must be an object", index)}
		}
		ticker := firstStringGeneric(row, "ticker", "symbol")
		if ticker == nil {
			continue
		}
		entries = append(entries, earningsCalendarEntry{
			Ticker:     strings.ToUpper(*ticker),
			ReportDate: firstDateGeneric(row, "reportDate", "report_date", "periodEnding", "date"),
			FilingDate: firstDateGeneric(row, "filingDate", "filing_date"),
		})
	}
	if len(entries) == 0 {
		return nil, nil
	}
	sort.SliceStable(entries, func(i, j int) bool { return calendarEntryNewer(entries[i], entries[j]) })
	seen := map[string]bool{}
	unique := make([]earningsCalendarEntry, 0, limit)
	for _, entry := range entries {
		if seen[entry.Ticker] {
			continue
		}
		seen[entry.Ticker] = true
		unique = append(unique, entry)
		if len(unique) >= limit {
			break
		}
	}
	return unique, nil
}

func calendarEntryNewer(a, b earningsCalendarEntry) bool {
	aFiling, bFiling := a.FilingDate != nil, b.FilingDate != nil
	if aFiling != bFiling {
		return aFiling
	}
	if aFiling {
		if !a.FilingDate.Equal(*b.FilingDate) {
			return a.FilingDate.After(*b.FilingDate)
		}
	}
	aReport, bReport := a.ReportDate != nil, b.ReportDate != nil
	if aReport != bReport {
		return aReport
	}
	if aReport && !a.ReportDate.Equal(*b.ReportDate) {
		return a.ReportDate.After(*b.ReportDate)
	}
	return a.Ticker > b.Ticker
}

func firstDateGeneric(record map[string]any, keys ...string) *time.Time {
	for _, key := range keys {
		s, ok := record[key].(string)
		if !ok || s == "" {
			continue
		}
		text := s
		if len(text) > 10 {
			text = text[:10]
		}
		if t, err := time.Parse(dateLayout, text); err == nil {
			return &t
		}
	}
	return nil
}

// --- get_insider_trades ---

func (c *callCtx) getInsiderTrades(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	formTypeArg, err := argString(args, "form_type")
	if err != nil {
		return Result{}, err
	}
	if formTypeArg != nil {
		return Result{}, &providers.InputError{Msg: "form_type filtering is not supported: the validated SECForm4 route " +
			"does not report form types"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 100)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 15)
	if err != nil {
		return Result{}, err
	}
	nameArg, err := argString(args, "name")
	if err != nil {
		return Result{}, err
	}
	transactionTypeArg, err := argString(args, "transaction_type")
	if err != nil {
		return Result{}, err
	}
	filingDateArg, err := argString(args, "filing_date")
	if err != nil {
		return Result{}, err
	}
	filingDateGTEArg, err := argString(args, "filing_date_gte")
	if err != nil {
		return Result{}, err
	}
	filingDateLTEArg, err := argString(args, "filing_date_lte")
	if err != nil {
		return Result{}, err
	}
	if _, verr := validateDate(filingDateArg, "filing_date"); verr != nil {
		return Result{}, verr
	}
	if _, verr := validateDate(filingDateGTEArg, "filing_date_gte"); verr != nil {
		return Result{}, verr
	}
	if _, verr := validateDate(filingDateLTEArg, "filing_date_lte"); verr != nil {
		return Result{}, verr
	}
	run, err := c.run(secform4, insiderEndpoint, nil, map[string]any{"query": symbol})
	if err != nil {
		return Result{}, err
	}
	trades, err := providers.NormalizeInsiderTrades(run.Output, symbol, limit, nameArg, transactionTypeArg,
		filingDateArg, filingDateGTEArg, filingDateLTEArg)
	if err != nil {
		return Result{}, err
	}
	out := make([]any, len(trades))
	for i, t := range trades {
		out[i] = t
	}
	return Result{Value: out, WrapperKey: "insider_trades", Paginate: true}, nil
}

// --- screen_stocks / list_stock_screener_filters ---

func (c *callCtx) screenStocks(args map[string]any) (Result, error) {
	currencyArg, err := argString(args, "currency")
	if err != nil {
		return Result{}, err
	}
	if currencyArg != nil && *currencyArg != "USD" {
		return Result{}, &providers.InputError{Msg: "only USD currency is supported by the Monid-backed server."}
	}
	filtersArg, present, err := argObjectSlice(args, "filters")
	if err != nil {
		return Result{}, err
	}
	if !present {
		return Result{}, &providers.InputError{Msg: "filters is required and must include exchange and/or market_cap"}
	}
	limitRaw, err := argIntDefault(args, "limit", 10)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 100)
	if err != nil {
		return Result{}, err
	}
	request, err := providers.ValidateScreenerRequest(filtersArg, limit, 0)
	if err != nil {
		return Result{}, err
	}
	queryParams := make(map[string]any, len(request.QueryParams))
	for k, v := range request.QueryParams {
		queryParams[k] = v
	}
	run, err := c.run(nasdaq, screenerEndpoint, nil, queryParams)
	if err != nil {
		return Result{}, err
	}
	rows, err := providers.NormalizeScreener(run.Output)
	if err != nil {
		return Result{}, err
	}
	var exchange *string
	if v, ok := request.QueryParams["exchange"]; ok {
		exchange = &v
	}
	results := providers.BuildSearchResults(rows, exchange, limit)
	out := make([]any, len(results))
	for i, r := range results {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "search_results"}, nil
}

func (c *callCtx) listStockScreenerFilters(args map[string]any) (Result, error) {
	return Result{Value: providers.ScreenerFilters()}, nil
}

// --- get_filing_items / list_filing_item_types ---

func (c *callCtx) getFilingItems(args map[string]any) (Result, error) {
	includeExhibits, err := argBoolDefault(args, "include_exhibits", false)
	if err != nil {
		return Result{}, err
	}
	if includeExhibits {
		return Result{}, &providers.InputError{Msg: "include_exhibits is not supported: the validated route cannot " +
			"identify and fetch filing exhibits"}
	}
	accessionArg, err := argString(args, "accession_number")
	if err != nil {
		return Result{}, err
	}
	normalizedAccession, err := providers.NormalizeAccession(accessionArg)
	if err != nil {
		return Result{}, err
	}
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	filingTypeArg, err := argString(args, "filing_type")
	if err != nil {
		return Result{}, err
	}
	if filingTypeArg == nil {
		return Result{}, &providers.InputError{Msg: "filing_type is required"}
	}
	yearArg, err := argInt(args, "year")
	if err != nil {
		return Result{}, err
	}
	quarterArg, err := argInt(args, "quarter")
	if err != nil {
		return Result{}, err
	}
	itemArg, err := argString(args, "item")
	if err != nil {
		return Result{}, err
	}

	var symbol, normalizedType string
	var selectedYear int
	var selectedQuarter *int
	var selectedItem *providers.FilingItem
	var filingsRun *monid.Run

	if yearArg == nil {
		symbol, err = validateTicker(*tickerArg)
		if err != nil {
			return Result{}, err
		}
		normalizedType, err = providers.ValidateCatalogFilingType(*filingTypeArg)
		if err != nil {
			return Result{}, err
		}
		if quarterArg != nil && (*quarterArg < 1 || *quarterArg > 4) {
			return Result{}, &providers.InputError{Msg: "quarter must be between 1 and 4"}
		}
		if itemArg != nil {
			resolved, rerr := providers.ResolveItem(normalizedType, *itemArg)
			if rerr != nil {
				return Result{}, rerr
			}
			selectedItem = &resolved
		}
		filingsRun, err = c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": symbol, "country": "US"})
		if err != nil {
			return Result{}, err
		}
		filingsValue, uerr := unmarshalRun(filingsRun)
		if uerr != nil {
			return Result{}, uerr
		}
		year, yerr := latestFilingYear(filingsValue, normalizedType, quarterArg, normalizedAccession)
		if yerr != nil {
			return Result{}, yerr
		}
		selectedQuarter = quarterArg
		if year == nil {
			return Result{Value: fd.NewErrorResponse("not_found", "No "+normalizedType+" filing matches ticker "+symbol+".")}, nil
		}
		selectedYear = *year
	} else {
		symbol, normalizedType, selectedYear, selectedQuarter, selectedItem, err = providers.ValidateFilingItemRequest(
			*tickerArg, *filingTypeArg, *yearArg, quarterArg, itemArg)
		if err != nil {
			return Result{}, err
		}
		filingsRun, err = c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": symbol, "country": "US"})
		if err != nil {
			return Result{}, err
		}
	}

	filingsValue, err := unmarshalRun(filingsRun)
	if err != nil {
		return Result{}, err
	}
	selection, err := providers.SelectFiling(filingsValue, normalizedType, selectedYear, selectedQuarter, normalizedAccession)
	if err != nil {
		return Result{}, err
	}
	if selection.Filing == nil {
		message := fmt.Sprintf("No %s filing matches ticker %s, year %d", normalizedType, symbol, selectedYear)
		if selectedQuarter != nil {
			message += fmt.Sprintf(", quarter %d.", *selectedQuarter)
		} else {
			message += "."
		}
		return Result{Value: fd.NewErrorResponse("not_found", message)}, nil
	}
	selected := *selection.Filing
	sourceURL, err := providers.ValidateSECURL(selected.SourceURL)
	if err != nil {
		return Result{}, &monid.RunError{Kind: monid.ErrProviderHTTP, Message: err.Error()}
	}
	// timeoutMS: 30_000 here matches service.py's get_filing_items scrape
	// call exactly; other scrape call sites (interest rates, index fund)
	// use 60_000.
	scrapeRun, err := c.run(contextDev, scrapeEndpoint, nil, map[string]any{
		"url": sourceURL, "includeLinks": false, "includeImages": false,
		"useMainContentOnly": true, "timeoutMS": 30_000,
	})
	if err != nil {
		return Result{}, err
	}
	scrapeValue, err := unmarshalRun(scrapeRun)
	if err != nil {
		return Result{}, err
	}
	markdown, _, err := providers.ParseScrapePayload(scrapeValue, sourceURL)
	if err != nil {
		return Result{}, err
	}
	sections, err := providers.ParseFilingSections(markdown, normalizedType, selectedItem)
	if err != nil {
		return Result{}, err
	}
	items := make([]fd.FilingItem, 0, len(sections))
	for _, section := range sections {
		number, _ := section["item"].(string)
		name, _ := section["title"].(string)
		text, _ := section["content"].(string)
		items = append(items, providers.FilingItemRecord(number, name, text))
	}
	if selectedItem != nil && len(items) == 0 {
		return Result{Value: fd.NewErrorResponse("not_found",
			fmt.Sprintf("Filing %s has no item %s.", selected.AccessionNumber, selectedItem.Name))}, nil
	}
	reportDay, err := time.Parse(dateLayout, selected.ReportDate)
	if err != nil {
		return Result{}, &providers.SchemaDriftError{Msg: "selected filing report_date is not ISO-8601"}
	}
	var accessionPtr *string
	if selected.AccessionNumber != "" {
		accession := selected.AccessionNumber
		accessionPtr = &accession
	}
	var quarterPtr *int
	if normalizedType != "10-K" {
		q := ((int(reportDay.Month()) - 1) / 3) + 1
		quarterPtr = &q
	}
	response := providers.BuildFilingItemsResponse(sourceURL, symbol, selected.Form, accessionPtr, reportDay.Year(), quarterPtr, items)
	return Result{Value: response}, nil
}

// latestFilingYear mirrors service._latest_filing_year: the newest year
// with a filing matching filingType (and, if given, quarter/accession),
// used only when get_filing_items receives year=nil.
func latestFilingYear(value any, filingType string, quarter *int, accessionNumber *string) (*int, error) {
	records, err := filingRecordsRaw(value)
	if err != nil {
		return nil, err
	}
	var best *int
	for _, record := range records {
		form, ok := record["form"].(string)
		if !ok || strings.ToUpper(strings.TrimSpace(form)) != filingType {
			continue
		}
		reportDate, ok := record["reportDate"].(string)
		if !ok {
			continue
		}
		text := reportDate
		if len(text) > 10 {
			text = text[:10]
		}
		day, perr := time.Parse(dateLayout, text)
		if perr != nil {
			continue
		}
		if quarter != nil {
			q := ((int(day.Month()) - 1) / 3) + 1
			if q != *quarter {
				continue
			}
		}
		if accessionNumber != nil {
			sourceURL, ok := record["primaryDocumentUrl"].(string)
			if !ok {
				continue
			}
			acc := providers.DeriveAccession(sourceURL)
			if acc == nil || *acc != *accessionNumber {
				continue
			}
		}
		year := day.Year()
		if best == nil || year > *best {
			y := year
			best = &y
		}
	}
	return best, nil
}

// filingRecordsRaw mirrors service._filing_records: unwraps a DefiLlama
// filings payload into its row list, descending through "data"/"filings"
// wrapper keys up to four levels.
func filingRecordsRaw(value any) ([]map[string]any, error) {
	current := value
	for i := 0; i < 4; i++ {
		if arr, ok := current.([]any); ok {
			records := make([]map[string]any, 0, len(arr))
			for index, item := range arr {
				obj, ok := item.(map[string]any)
				if !ok {
					return nil, &providers.SchemaDriftError{Msg: fmt.Sprintf("filing row %d is not an object", index)}
				}
				records = append(records, obj)
			}
			return records, nil
		}
		obj, ok := current.(map[string]any)
		if !ok {
			break
		}
		advanced := false
		for _, key := range []string{"data", "filings"} {
			child, exists := obj[key]
			if !exists {
				continue
			}
			switch child.(type) {
			case []any, map[string]any:
				current = child
				advanced = true
			}
			if advanced {
				break
			}
		}
		if !advanced {
			break
		}
	}
	return nil, &providers.SchemaDriftError{Msg: "DefiLlama payload omitted filing records"}
}

func (c *callCtx) listFilingItemTypes(args map[string]any) (Result, error) {
	filingTypeArg, err := argString(args, "filing_type")
	if err != nil {
		return Result{}, err
	}
	response, err := providers.ListFilingItemTypes(filingTypeArg)
	if err != nil {
		return Result{}, err
	}
	return Result{Value: response}, nil
}

// --- get_segmented_financials ---

func (c *callCtx) getSegmentedFinancials(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	periodRaw, err := argStringDefault(args, "period", "annual")
	if err != nil {
		return Result{}, err
	}
	period, err := validatePeriod(periodRaw)
	if err != nil {
		return Result{}, err
	}
	if period != "annual" {
		return Result{}, &providers.InputError{Msg: "period must be annual: the validated route extracts the annual 10-K"}
	}
	limitRaw, err := argIntDefault(args, "limit", 4)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 100)
	if err != nil {
		return Result{}, err
	}
	report, err := parseDateFilterGroup(args, "report_period")
	if err != nil {
		return Result{}, err
	}

	filingsRun, err := c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": symbol, "country": "US"})
	if err != nil {
		return Result{}, err
	}
	filings, err := providers.NormalizeFilings(filingsRun.Output, symbol, nil, 10_000, nil, nil)
	if err != nil {
		return Result{}, err
	}
	filing := latestFilingByForm(filings, "10-K")
	if filing == nil {
		return Result{Value: fd.NewErrorResponse("not_found", "No 10-K filing exists for ticker "+symbol+".")}, nil
	}
	filingURL := ""
	if filing.URL != nil {
		filingURL = *filing.URL
	}
	accession := providers.DeriveAccession(filingURL)

	extractRun, err := c.run(contextDev, extractEndpoint,
		extractRequestBody(filingURL, segmentExtractSchema(), segmentInstructions), nil)
	if err != nil {
		return Result{}, err
	}
	data, err := parseExtractOutput(extractRun.Output)
	if err != nil {
		return Result{}, err
	}
	records, err := normalizeSegmentedFinancials(data, symbol, filingURL, accession)
	if err != nil {
		return Result{}, err
	}
	out := make([]any, 0, len(records))
	for _, record := range records {
		if !report.any() || report.matches(record.ReportPeriod) {
			out = append(out, record.Object)
		}
		if len(out) >= limit {
			break
		}
	}
	return Result{Value: out, WrapperKey: "segmented_financials", Paginate: true}, nil
}

// latestFilingByForm mirrors the shared shape of service._latest_ten_k and
// service._latest_kpi_filing: the most recent SEC-valid filing of one form,
// by (report_date, filing_date) descending.
func latestFilingByForm(filings []fd.Filing, wantedForm string) *fd.Filing {
	var best *fd.Filing
	var bestReport, bestFiling string
	for i := range filings {
		f := filings[i]
		if f.FilingType == nil || strings.ToUpper(*f.FilingType) != wantedForm {
			continue
		}
		if f.URL == nil {
			continue
		}
		if _, verr := providers.ValidateSECURL(*f.URL); verr != nil {
			continue
		}
		if f.ReportDate == nil || f.FilingDate == nil {
			continue
		}
		if best == nil || *f.ReportDate > bestReport || (*f.ReportDate == bestReport && *f.FilingDate > bestFiling) {
			fCopy := f
			best = &fCopy
			bestReport, bestFiling = *f.ReportDate, *f.FilingDate
		}
	}
	return best
}

// --- get_kpi_metrics / get_kpi_guidance / get_kpi_non_gaap ---

// kpiExtractArgs is the shared, pre-validated argument set for the three
// KPI extraction tools, mirroring their identical parameter lists.
type kpiExtractArgs struct {
	ticker     string
	period     string
	metricName *string
	gte        *time.Time
	gteStr     *string
	lte        *time.Time
	limit      int
}

func parseKPIExtractArgs(args map[string]any) (kpiExtractArgs, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return kpiExtractArgs{}, err
	}
	if tickerArg == nil {
		return kpiExtractArgs{}, &providers.InputError{Msg: "ticker is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return kpiExtractArgs{}, err
	}
	periodRaw, err := argStringDefault(args, "period", "quarterly")
	if err != nil {
		return kpiExtractArgs{}, err
	}
	period, err := validateKPIPeriod(periodRaw)
	if err != nil {
		return kpiExtractArgs{}, err
	}
	metricName, err := argString(args, "metric_name")
	if err != nil {
		return kpiExtractArgs{}, err
	}
	gteArg, err := argString(args, "report_period_gte")
	if err != nil {
		return kpiExtractArgs{}, err
	}
	gte, err := validateDate(gteArg, "report_period_gte")
	if err != nil {
		return kpiExtractArgs{}, err
	}
	lteArg, err := argString(args, "report_period_lte")
	if err != nil {
		return kpiExtractArgs{}, err
	}
	lte, err := validateDate(lteArg, "report_period_lte")
	if err != nil {
		return kpiExtractArgs{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 4)
	if err != nil {
		return kpiExtractArgs{}, err
	}
	limit, err := validateLimit(limitRaw, 50)
	if err != nil {
		return kpiExtractArgs{}, err
	}
	return kpiExtractArgs{ticker: symbol, period: period, metricName: metricName, gte: gte, lte: lte, limit: limit}, nil
}

// kpiFiling fetches the filings run, joins the latest matching 10-K/10-Q,
// and runs the /web/extract call, mirroring the shared prologue of
// service._kpi_extract_response (through parse_extract_output). ok is
// false when no matching filing exists (the caller renders a not_found FD
// error) or the filing's report_period falls outside [gte, lte] (the
// caller renders an empty list, per Python's early
// `list_response(response_key, [], None)` return).
func (c *callCtx) kpiFiling(parsed kpiExtractArgs, schema map[string]any, instructions string) (data map[string]any, filingURL string, found, inRange bool, err error) {
	filingsRun, rerr := c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": parsed.ticker, "country": "US"})
	if rerr != nil {
		return nil, "", false, false, rerr
	}
	filings, nerr := providers.NormalizeFilings(filingsRun.Output, parsed.ticker, nil, 10_000, nil, nil)
	if nerr != nil {
		return nil, "", false, false, nerr
	}
	wanted := "10-Q"
	if parsed.period == "annual" {
		wanted = "10-K"
	}
	filing := latestFilingByForm(filings, wanted)
	if filing == nil {
		return nil, "", false, false, nil
	}
	if filing.ReportDate != nil {
		reportDay, perr := time.Parse(dateLayout, *filing.ReportDate)
		if perr == nil {
			if (parsed.gte != nil && reportDay.Before(*parsed.gte)) || (parsed.lte != nil && reportDay.After(*parsed.lte)) {
				return nil, "", true, false, nil
			}
		}
	}
	if filing.URL != nil {
		filingURL = *filing.URL
	}
	extractRun, cerr := c.run(contextDev, extractEndpoint, extractRequestBody(filingURL, schema, instructions), nil)
	if cerr != nil {
		return nil, "", true, true, cerr
	}
	data, perr := parseExtractOutput(extractRun.Output)
	if perr != nil {
		return nil, "", true, true, perr
	}
	return data, filingURL, true, true, nil
}

func (c *callCtx) getKPIMetrics(args map[string]any) (Result, error) {
	parsed, err := parseKPIExtractArgs(args)
	if err != nil {
		return Result{}, err
	}
	data, filingURL, found, inRange, err := c.kpiFiling(parsed, kpiMetricsExtractSchema(), kpiMetricsInstructions)
	if err != nil {
		return Result{}, err
	}
	if !found {
		wanted := "10-Q"
		if parsed.period == "annual" {
			wanted = "10-K"
		}
		return Result{Value: fd.NewErrorResponse("not_found", "No "+wanted+" filing exists for ticker "+parsed.ticker+".")}, nil
	}
	if !inRange {
		return Result{Value: []any{}, WrapperKey: "kpi_metrics", Paginate: true}, nil
	}
	records, err := normalizeKPIMetrics(data, parsed.ticker, filingURL, &parsed.period, parsed.metricName)
	if err != nil {
		return Result{}, err
	}
	if len(records) > parsed.limit {
		records = records[:parsed.limit]
	}
	out := make([]any, len(records))
	for i, r := range records {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "kpi_metrics", Paginate: true}, nil
}

func (c *callCtx) getKPIGuidance(args map[string]any) (Result, error) {
	parsed, err := parseKPIExtractArgs(args)
	if err != nil {
		return Result{}, err
	}
	data, filingURL, found, inRange, err := c.kpiFiling(parsed, kpiGuidanceExtractSchema(), kpiGuidanceInstructions)
	if err != nil {
		return Result{}, err
	}
	if !found {
		wanted := "10-Q"
		if parsed.period == "annual" {
			wanted = "10-K"
		}
		return Result{Value: fd.NewErrorResponse("not_found", "No "+wanted+" filing exists for ticker "+parsed.ticker+".")}, nil
	}
	if !inRange {
		return Result{Value: []any{}, WrapperKey: "kpi_guidance", Paginate: true}, nil
	}
	records, err := normalizeKPIGuidance(data, parsed.ticker, filingURL, &parsed.period, parsed.metricName)
	if err != nil {
		return Result{}, err
	}
	if len(records) > parsed.limit {
		records = records[:parsed.limit]
	}
	out := make([]any, len(records))
	for i, r := range records {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "kpi_guidance", Paginate: true}, nil
}

func (c *callCtx) getKPINonGAAP(args map[string]any) (Result, error) {
	parsed, err := parseKPIExtractArgs(args)
	if err != nil {
		return Result{}, err
	}
	data, filingURL, found, inRange, err := c.kpiFiling(parsed, kpiNonGAAPExtractSchema(), kpiNonGAAPInstructions)
	if err != nil {
		return Result{}, err
	}
	if !found {
		wanted := "10-Q"
		if parsed.period == "annual" {
			wanted = "10-K"
		}
		return Result{Value: fd.NewErrorResponse("not_found", "No "+wanted+" filing exists for ticker "+parsed.ticker+".")}, nil
	}
	if !inRange {
		return Result{Value: []any{}, WrapperKey: "kpi_non_gaap", Paginate: true}, nil
	}
	records, err := normalizeKPINonGAAP(data, parsed.ticker, filingURL, &parsed.period, parsed.metricName)
	if err != nil {
		return Result{}, err
	}
	if len(records) > parsed.limit {
		records = records[:parsed.limit]
	}
	out := make([]any, len(records))
	for i, r := range records {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "kpi_non_gaap", Paginate: true}, nil
}

// --- get_interest_rates ---

// getInterestRates mirrors service.get_interest_rates. Per this port's
// brief, the four central-bank scrapes fan out over goroutines instead of
// the Python source's sequential loop; a bank whose page can't be fetched
// or parsed is omitted, exactly as Python's `except (UpstreamError,
// SchemaDriftError): continue` does, and never fails the whole call.
func (c *callCtx) getInterestRates(args map[string]any) (Result, error) {
	results := make([]*fd.InterestRate, len(bankSpecs))
	var wg sync.WaitGroup
	wg.Add(len(bankSpecs))
	for i, spec := range bankSpecs {
		go func(i int, spec bankSpec) {
			defer wg.Done()
			run, err := c.run(contextDev, scrapeEndpoint, nil, interestRateScrapeQuery(spec.URL))
			if err != nil {
				return
			}
			markdown, perr := parseInterestRateScrapeMarkdown(run.Output, spec.URL)
			if perr != nil {
				return
			}
			rate := parsePolicyRate(markdown, spec.Bank)
			if rate == nil {
				return
			}
			bank, name, value := rate.Bank, rate.Name, rate.Rate
			results[i] = &fd.InterestRate{Bank: &bank, Name: &name, Rate: &value, Date: rate.Date}
		}(i, spec)
	}
	wg.Wait()
	records := make([]any, 0, len(bankSpecs))
	for _, r := range results {
		if r != nil {
			records = append(records, *r)
		}
	}
	return Result{Value: records, WrapperKey: "interest_rates"}, nil
}

// --- get_index_fund ---

func (c *callCtx) getIndexFund(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	asOfArg, err := argString(args, "as_of")
	if err != nil {
		return Result{}, err
	}
	asOfDate, err := validateDate(asOfArg, "as_of")
	if err != nil {
		return Result{}, err
	}
	assetClassArg, err := argString(args, "asset_class")
	if err != nil {
		return Result{}, err
	}
	normalizedClass, err := validateAssetClass(assetClassArg)
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 50)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 1000)
	if err != nil {
		return Result{}, err
	}
	offset, err := argIntDefault(args, "offset", 0)
	if err != nil {
		return Result{}, err
	}
	if offset < 0 {
		return Result{}, &providers.InputError{Msg: "offset must be a non-negative integer"}
	}

	searchRun, err := c.run(contextDev, indexFundSearchEndpoint, indexFundSearchRequestBody(symbol), nil)
	if err != nil {
		return Result{}, err
	}
	candidates, err := pickHoldingsCandidates(searchRun.Output, symbol)
	if err != nil {
		return Result{}, err
	}
	var markdown string
	var title *string
	found := false
	max := len(candidates)
	if max > 3 {
		max = 3
	}
	for _, candidate := range candidates[:max] {
		run, rerr := c.run(contextDev, scrapeEndpoint, nil, indexFundScrapeQuery(candidate.URL))
		if rerr != nil {
			continue
		}
		pageMarkdown, perr := parseIndexFundScrapeMarkdown(run.Output, candidate.URL)
		if perr != nil {
			continue
		}
		if len(parseFundHoldings(pageMarkdown)) > 0 {
			markdown = pageMarkdown
			title = candidate.Title
			found = true
			break
		}
	}
	if !found {
		return Result{Value: fd.NewErrorResponse("bad_request", "holdings document not routable for "+symbol)}, nil
	}
	holdings := parseFundHoldings(markdown)
	if normalizedClass != nil {
		filtered := make([]fd.FundHolding, 0, len(holdings))
		for _, h := range holdings {
			if h.AssetClass != nil && *h.AssetClass == *normalizedClass {
				filtered = append(filtered, h)
			}
		}
		holdings = filtered
	}
	documentAsOf := parseFundAsOf(markdown)
	if asOfDate != nil && documentAsOf != nil {
		if documentDay, perr := time.Parse(dateLayout, *documentAsOf); perr == nil && documentDay.After(*asOfDate) {
			return Result{Value: fd.NewErrorResponse("not_found",
				"No holdings composition in effect on or before "+*asOfArg+" is routable for "+symbol+".")}, nil
		}
	}
	end := offset + limit
	if end > len(holdings) {
		end = len(holdings)
	}
	var pageHoldings []fd.FundHolding
	if offset < len(holdings) {
		pageHoldings = holdings[offset:end]
	}

	fund := newOrderedJSONObject()
	if title != nil {
		fund.set("name", *title)
	}
	if documentAsOf != nil {
		fund.set("as_of", *documentAsOf)
	}
	fund.set("source", "public fund holdings fact sheet (markdown)")
	fund.set("total_holdings", len(holdings))
	fund.set("returned", len(pageHoldings))
	fund.set("offset", offset)

	holdingsAny := make([]any, len(pageHoldings))
	for i, h := range pageHoldings {
		holdingsAny[i] = h
	}
	response := newOrderedJSONObject()
	response.set("ticker", symbol)
	response.set("fund", fund)
	response.set("holdings", holdingsAny)
	return Result{Value: response}, nil
}

// --- get_institutional_holdings ---

func (c *callCtx) getInstitutionalHoldings(args map[string]any) (Result, error) {
	filerCIKArg, err := argString(args, "filer_cik")
	if err != nil {
		return Result{}, err
	}
	if filerCIKArg != nil {
		return Result{Value: fd.NewErrorResponse("bad_request", "filer_cik lookup is not routed; pass ticker instead")}, nil
	}
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 10)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 200)
	if err != nil {
		return Result{}, err
	}
	report, err := parseDateFilterGroup(args, "report_period")
	if err != nil {
		return Result{}, err
	}
	// SECForm4's institution-holders route is keyed on CIK, not ticker.
	// This passed {"ticker": symbol} and the provider answered HTTP 422
	// ("cik parameter is required") on every call, so this route was
	// returning upstream_error in production. Verified live 2026-09-04:
	// cik=320193 answers HTTP 200; symbol=AAPL completes with no output;
	// no parameters at all is the 422. The CIK comes from the same
	// filings-backed lookup get_insider_ownership uses, and reuses its
	// cache entry.
	cik, found, err := c.resolveIssuerCIK(symbol)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{Value: fd.NewErrorResponse("not_found",
			"No SEC CIK could be resolved for ticker "+symbol+".")}, nil
	}
	run, err := c.run(secform4, institutionalEndpoint, nil, map[string]any{"cik": cik})
	if err != nil {
		return Result{}, err
	}
	holdings, err := normalizeInstitutionalHoldings(run.Output, symbol, limit, report)
	if err != nil {
		return Result{}, err
	}
	out := make([]any, len(holdings))
	for i, h := range holdings {
		out[i] = h
	}
	return Result{Value: out, WrapperKey: "institutional_holdings", Paginate: true}, nil
}
