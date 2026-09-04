// This file sources the cash flow statement from marketbeat's
// /get_financial_statements route.
//
// Why not the normalized statements feed every other statement uses.
// Diffed against SEC XBRL across eight large caps (AAPL, MSFT, XOM, KO,
// TSLA, PFE, VZ, NVDA) on 2026-09-04, that feed's cash flow subtotals
// came back:
//
//	operating activities   8/8 correct
//	investing activities   0/8 correct
//	financing activities   4/8 correct
//	net change in cash     0/8 correct
//
// The error is not a constant offset or a sign flip. Apple FY2025
// investing read 27,910,000,000 against the 10-K's 15,195,000,000, and
// Microsoft FY2026 read -23,552,000,000 against SEC's -139,500,000,000.
// Three of four subtotals being unreliable rules out patching individual
// fields, so this statement is sourced whole from marketbeat, which
// matched SEC line for line on every figure checked.
//
// The two sources are NOT mixed in one record. Keeping the fields
// marketbeat does not carry from a feed proven wrong on half its
// subtotals would produce a record that is right where it was checked and
// unverified everywhere else. share_based_compensation and
// ending_cash_balance are therefore omitted rather than carried over.
//
// Sign conventions differ and are normalized here. Financial Datasets
// reports capital_expenditure and dividends as positive magnitudes;
// marketbeat reports both as the negative cash outflows they are. The
// three subtotals share a sign convention already.
package service

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/monid"
	"github.com/belazy/monid-finance/providers"
)

const (
	marketbeat                     = "marketbeat"
	marketbeatStatementsEndpoint   = "/get_financial_statements"
	marketbeatCashFlowAnnualPrefix = "Annual Cash Flow Statements"
	marketbeatCashFlowQuarterly    = "Quarterly Cash Flow Statements"
	// marketbeatScale converts marketbeat's reported units, which are
	// millions, to the whole dollars every other record in this package
	// uses. Verified against Apple FY2025: the feed prints 111,482 where
	// the 10-K reports 111,482,000,000.
	marketbeatScale = 1e6
	// marketbeatPeriodEndMetric is the row carrying each column's fiscal
	// period end. It is the real close (Apple prints 9/27/2025), not the
	// month-end rounding the normalized feed applies.
	marketbeatPeriodEndMetric = "Period end date"
)

// The marketbeat rows carrying each Financial Datasets cash flow field.
// A field with no row here is one marketbeat does not report, and is
// omitted rather than guessed.
const (
	mbNetIncome    = "Net Income / (Loss) Continuing Operations"
	mbDepreciation = "Depreciation Expense"
	mbOperating    = "Net Cash From Operating Activities"
	mbCapex        = "Purchase of Property, Plant & Equipment"
	mbInvesting    = "Net Cash From Investing Activities"
	mbDividends    = "Payment of Dividends"
	mbFinancing    = "Net Cash From Financing Activities"
	mbChangeInCash = "Net Change in Cash & Equivalents"
)

// marketbeatColumn is one reporting period from a marketbeat statement
// table, already scaled to whole units and keyed by metric name.
type marketbeatColumn struct {
	periodEnd time.Time
	values    map[string]float64
}

// fetchMarketbeatCashFlow returns one issuer's cash flow columns, newest
// first, for the requested period.
func (c *callCtx) fetchMarketbeatCashFlow(symbol, period string) ([]marketbeatColumn, *int, error) {
	run, err := c.run(marketbeat, marketbeatStatementsEndpoint, nil, map[string]any{"ticker": symbol})
	if err != nil {
		return nil, nil, err
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Statements map[string][]map[string]any `json:"statements"`
		} `json:"data"`
	}
	if err := json.Unmarshal(run.Output, &payload); err != nil {
		return nil, nil, &providers.SchemaDriftError{Msg: "marketbeat statements payload must be an object"}
	}
	if payload.Status != "success" {
		return nil, nil, &providers.SchemaDriftError{Msg: "marketbeat statements status must be 'success'"}
	}

	// Table names embed the company ("Annual Cash Flow Statements for
	// Apple"), so they are matched by prefix rather than by equality.
	wantPrefix := marketbeatCashFlowAnnualPrefix
	if period == "quarterly" {
		wantPrefix = marketbeatCashFlowQuarterly
	}
	var rows, annualRows []map[string]any
	for name, table := range payload.Data.Statements {
		if strings.HasPrefix(name, wantPrefix) {
			rows = table
		}
		if strings.HasPrefix(name, marketbeatCashFlowAnnualPrefix) {
			annualRows = table
		}
	}
	if rows == nil {
		return nil, nil, &providers.SchemaDriftError{
			Msg: "marketbeat returned no " + strings.ToLower(wantPrefix) + " table for " + symbol}
	}
	columns, err := marketbeatColumns(rows)
	if err != nil {
		return nil, nil, err
	}
	// The fiscal year end comes from the ANNUAL table's period ends, which
	// are true year ends, whichever period the caller asked for. Reading
	// it from the quarterly table gave the newest quarter's month instead,
	// which labelled Apple's June quarter Q4 rather than Q3.
	var fiscalEnd *int
	if annualRows != nil {
		if annual, aerr := marketbeatColumns(annualRows); aerr == nil && len(annual) > 0 {
			month := int(annual[0].periodEnd.Month())
			fiscalEnd = &month
		}
	}
	return columns, fiscalEnd, nil
}

// marketbeatColumns pivots marketbeat's metric-per-row table into one
// column per reporting period, newest first.
func marketbeatColumns(rows []map[string]any) ([]marketbeatColumn, error) {
	byMetric := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		metric, _ := row["Metric"].(string)
		if metric != "" {
			byMetric[metric] = row
		}
	}
	periodRow, ok := byMetric[marketbeatPeriodEndMetric]
	if !ok {
		return nil, &providers.SchemaDriftError{
			Msg: "marketbeat cash flow table omitted its " + marketbeatPeriodEndMetric + " row"}
	}

	// Column labels differ by table: the annual table is keyed by fiscal
	// year ("2025"), the quarterly one by quarter ("Q3 2025"), and both
	// carry a junk "col_1". The period-end ROW is the reliable key: any
	// column whose period end parses as a date is a real period, and
	// sorting by that date puts the newest first regardless of label.
	type dated struct {
		label string
		end   time.Time
	}
	periods := make([]dated, 0, len(periodRow))
	for label, raw := range periodRow {
		if label == "Metric" {
			continue
		}
		if end, ok := marketbeatDate(raw); ok {
			periods = append(periods, dated{label: label, end: end})
		}
	}
	if len(periods) == 0 {
		return nil, &providers.SchemaDriftError{Msg: "marketbeat cash flow table carried no dated period columns"}
	}
	sort.Slice(periods, func(i, j int) bool { return periods[i].end.After(periods[j].end) })

	columns := make([]marketbeatColumn, 0, len(periods))
	for _, p := range periods {
		values := make(map[string]float64, len(byMetric))
		for metric, row := range byMetric {
			if metric == marketbeatPeriodEndMetric {
				continue
			}
			if v, ok := marketbeatNumber(row[p.label]); ok {
				values[metric] = v
			}
		}
		columns = append(columns, marketbeatColumn{periodEnd: p.end, values: values})
	}
	return columns, nil
}

// marketbeatNumber parses one reported figure and scales it to whole
// units. Blank, "-" and unparseable cells report absent rather than zero:
// a missing line and a genuine zero are different facts.
func marketbeatNumber(raw any) (float64, bool) {
	text, ok := raw.(string)
	if !ok {
		return 0, false
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, ",", ""))
	if text == "" || text == "-" || text == "N/A" {
		return 0, false
	}
	// Some cells wrap a negative in parentheses instead of signing it.
	negative := false
	if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
		negative, text = true, strings.TrimSuffix(strings.TrimPrefix(text, "("), ")")
	}
	value, err := strconv.ParseFloat(strings.TrimPrefix(text, "$"), 64)
	if err != nil {
		return 0, false
	}
	if negative {
		value = -value
	}
	return value * marketbeatScale, true
}

// marketbeatDate parses marketbeat's M/D/YYYY period end. A column whose
// date will not parse is skipped by the caller: a period that cannot be
// dated cannot be filtered or joined to a filing.
func marketbeatDate(raw any) (time.Time, bool) {
	text, ok := raw.(string)
	if !ok {
		return time.Time{}, false
	}
	day, err := time.Parse("1/2/2006", strings.TrimSpace(text))
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// buildMarketbeatCashFlow shapes one column into a Financial Datasets
// cash flow record.
//
// capital_expenditure and dividends are negated: marketbeat reports both
// as the negative outflows they are, while Financial Datasets reports
// them as positive magnitudes (verified against their live AAPL FY2025
// response). free_cash_flow follows their definition too, operating minus
// that positive capex, which reproduces their 98,767,000,000 exactly.
func buildMarketbeatCashFlow(ticker, period string, col marketbeatColumn,
	fiscalPeriod *string, identity *FilingIdentity) fd.CashFlowStatement {
	reportPeriod := col.periodEnd.Format(dateLayout)
	rec := fd.CashFlowStatement{
		Ticker:       &ticker,
		ReportPeriod: &reportPeriod,
		FiscalPeriod: fiscalPeriod,
		Period:       &period,
	}
	setIdentity(&rec.AccessionNumber, &rec.FormType, &rec.FilingURL, &rec.FilingDate, identity)

	get := func(metric string) *float64 {
		if v, ok := col.values[metric]; ok {
			return &v
		}
		return nil
	}
	negated := func(metric string) *float64 {
		v := get(metric)
		if v == nil {
			return nil
		}
		flipped := -*v
		return &flipped
	}

	rec.NetIncome = get(mbNetIncome)
	rec.DepreciationAndAmortization = get(mbDepreciation)
	rec.NetCashFlowFromOperations = get(mbOperating)
	rec.CapitalExpenditure = negated(mbCapex)
	rec.NetCashFlowFromInvesting = get(mbInvesting)
	rec.DividendsAndOtherCashDistributions = negated(mbDividends)
	rec.NetCashFlowFromFinancing = get(mbFinancing)
	rec.ChangeInCashAndEquivalents = get(mbChangeInCash)
	if rec.NetCashFlowFromOperations != nil && rec.CapitalExpenditure != nil {
		fcf := *rec.NetCashFlowFromOperations - *rec.CapitalExpenditure
		rec.FreeCashFlow = &fcf
	}
	return rec
}

// marketbeatCashFlowResponse answers get_cash_flow_statement for the
// annual and quarterly periods, fetching marketbeat's statement table and
// the filings index concurrently so the record still carries its
// accession number, form type and filing URL.
func (c *callCtx) marketbeatCashFlowResponse(parsed statementArgs) (Result, error) {
	var columns []marketbeatColumn
	var fiscalEnd *int
	var cashErr error
	var filingsRun *monid.Run
	var filingsErr error
	concurrent2(
		func() { columns, fiscalEnd, cashErr = c.fetchMarketbeatCashFlow(parsed.ticker, parsed.period) },
		func() {
			filingsRun, filingsErr = c.run(defillama, filingsEndpoint, nil,
				map[string]any{"ticker": parsed.ticker, "country": "US"})
		},
	)
	if cashErr != nil {
		return Result{}, cashErr
	}
	identityMap, err := buildFilingIdentityMap(filingsRun, filingsErr, parsed.ticker, parsed.period != "quarterly")
	if err != nil {
		return Result{}, err
	}

	records := marketbeatCashFlowRecords(parsed, columns, fiscalEnd, identityMap)
	return Result{Value: records, WrapperKey: "cash_flow_statements", Paginate: true}, nil
}

// marketbeatCashFlowRecords filters, limits and shapes one issuer's
// marketbeat columns into Financial Datasets cash flow records.
func marketbeatCashFlowRecords(parsed statementArgs, columns []marketbeatColumn,
	fiscalEndMonth *int, identityMap map[string]FilingIdentity) []any {

	records := make([]any, 0, len(columns))
	for _, col := range columns {
		day := col.periodEnd
		if parsed.report.any() && !parsed.report.matches(day) {
			continue
		}
		identity := lookupFilingIdentity(identityMap, day)
		if parsed.filing.any() {
			if identity == nil || identity.FilingDate == nil || !parsed.filing.matches(*identity.FilingDate) {
				continue
			}
		}
		row := providers.PeriodRow{ReportPeriod: day}
		fiscalPeriod := providers.FiscalPeriodLabel(row, fiscalEndMonth, parsed.period == "annual")
		records = append(records, buildMarketbeatCashFlow(parsed.ticker, parsed.period, col, fiscalPeriod, identity))
		if len(records) == parsed.limit {
			break
		}
	}
	return records
}

// statementRecords builds one statement kind's records, routing the cash
// flow statement to marketbeat.
//
// This is the seam every caller must cross, and the reason it exists.
// buildStatementRecords is where "which statement kind" becomes "which
// fields", and three call sites reach it: statementResponse,
// getAllFinancials and mergedLineItemRows. Deciding the source above that
// point fixed get_cash_flow_statement while leaving the other two on the
// bad feed, so the same server answered Apple's FY2025 investing as
// 15,195,000,000 on one route and 27,910,000,000 on another. One server
// contradicting itself is worse than one wrong number.
//
// ttm still composes from the normalized feed: marketbeat reports filed
// periods only, and a trailing window is not one.
func (c *callCtx) statementRecords(kind string, parsed statementArgs, value any,
	identityMap map[string]FilingIdentity) ([]any, error) {
	if kind != "cash" {
		return buildStatementRecords(kind, parsed, value, identityMap)
	}
	if parsed.period == "ttm" {
		columns, _, err := c.fetchMarketbeatCashFlow(parsed.ticker, "quarterly")
		if err != nil {
			return nil, err
		}
		return marketbeatTTMRecords(parsed, columns), nil
	}
	columns, fiscalEnd, err := c.fetchMarketbeatCashFlow(parsed.ticker, parsed.period)
	if err != nil {
		return nil, err
	}
	return marketbeatCashFlowRecords(parsed, columns, fiscalEnd, identityMap), nil
}

// ttmQuarters is how many consecutive quarters make a trailing year.
const ttmQuarters = 4

// marketbeatTTMRecords sums four consecutive quarters into each trailing
// window, newest first.
//
// The normalized feed used to build these, which meant summing four
// quarters of the investing and net-change lines measured 0/8 correct
// against SEC: four wrong quarters make a wrong year. Every field on a
// cash flow statement is a flow, so summing is the right operation here,
// unlike the balance sheet where the trailing path carries balances
// forward instead.
//
// A window is emitted only when all four quarters are present, and a
// field only when all four report it. A three-quarter sum is not a
// trailing year, and neither is a sum with a hole in it.
func marketbeatTTMRecords(parsed statementArgs, columns []marketbeatColumn) []any {
	records := make([]any, 0, parsed.limit)
	for start := 0; start+ttmQuarters <= len(columns); start++ {
		window := columns[start : start+ttmQuarters]
		newest := window[0]
		if parsed.report.any() && !parsed.report.matches(newest.periodEnd) {
			continue
		}
		summed := marketbeatColumn{periodEnd: newest.periodEnd, values: map[string]float64{}}
		for _, metric := range []string{
			mbNetIncome, mbDepreciation, mbOperating, mbCapex,
			mbInvesting, mbDividends, mbFinancing, mbChangeInCash,
		} {
			total, complete := 0.0, true
			for _, col := range window {
				v, ok := col.values[metric]
				if !ok {
					complete = false
					break
				}
				total += v
			}
			if complete {
				summed.values[metric] = total
			}
		}
		// A trailing window spans more than one filing, so it carries no
		// filing identity and no fiscal period label, matching what the
		// normalized trailing path already returns.
		records = append(records, buildMarketbeatCashFlow(parsed.ticker, "ttm", summed, nil, nil))
		if len(records) == parsed.limit {
			break
		}
	}
	return records
}
