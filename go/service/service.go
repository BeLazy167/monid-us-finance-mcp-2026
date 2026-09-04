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
	catalogEndpoint          = "/equities/v1/companies-list"
	summaryEndpoint          = "/equities/v1/summary"
	statementsEndpoint       = "/equities/v1/statements"
	filingsEndpoint          = "/equities/v1/filings"
	ohlcvEndpoint            = "/equities/v1/ohlcv"
	contextDev               = "context.dev"
	newsEndpoint             = "/news/search"
	scrapeEndpoint           = "/web/scrape/markdown"
	indexFundSearchEndpoint  = "/web/search"
	secform4                 = "secform4"
	insiderEndpoint          = "/search"
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
	client := monid.NewClient(apiKey, s.http, s.allowlist, s.maxConcurrentRuns)
	cc := &callCtx{ctx: ctx, client: client, svc: s, tool: tool}
	return handler(cc, args)
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
	ticker        string
	period        string
	limit         int
	report        dateFilters
	filing        dateFilters
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

func (c *callCtx) getCashFlowStatement(args map[string]any) (Result, error) {
	if err := checkAsReported(args); err != nil {
		return Result{}, err
	}
	return c.statementResponse("cash", args)
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
	series, err := providers.ParseStatementSeries(value, statement)
	if err != nil {
		return Result{}, err
	}
	rows := statementRowsForPeriod(series, parsed.period, statement)
	endMonth := providers.FiscalYearEndMonth(series)

	identityMap, err := buildFilingIdentityMap(filingsRun, filingsErr, parsed.ticker, parsed.period != "quarterly")
	if err != nil {
		return Result{}, err
	}
	if identityMap == nil && parsed.filing.any() {
		return Result{}, &monid.RunError{Kind: monid.ErrProviderHTTP,
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
		var identity *FilingIdentity
		if identityMap != nil {
			if id, ok := identityMap[key]; ok {
				identity = &id
			}
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
	return Result{Value: records, WrapperKey: statementResponseKeys[statement], Paginate: true}, nil
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
		identity, ok := identityMap[row.ReportPeriod.Format(dateLayout)]
		if !ok || identity.FilingDate == nil {
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
		best[reportDay] = candidate{filingDate: filingDay, identity: identity}
	}
	out := make(map[string]FilingIdentity, len(best))
	for day, c := range best {
		out[day] = c.identity
	}
	return out, nil
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
