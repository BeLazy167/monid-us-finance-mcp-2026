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
	"fmt"
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

// marketbeatCashFlowMetrics maps a Financial Datasets field to the
// marketbeat row that carries it. Every field not listed here is one
// marketbeat does not report, and is omitted rather than guessed.
var marketbeatCashFlowMetrics = struct {
	netIncome, depreciation, operating, capex, investing, dividends, financing, change string
}{
	netIncome:    "Net Income / (Loss) Continuing Operations",
	depreciation: "Depreciation Expense",
	operating:    "Net Cash From Operating Activities",
	capex:        "Purchase of Property, Plant & Equipment",
	investing:    "Net Cash From Investing Activities",
	dividends:    "Payment of Dividends",
	financing:    "Net Cash From Financing Activities",
	change:       "Net Change in Cash & Equivalents",
}

// marketbeatColumn is one reporting period from a marketbeat statement
// table, already scaled to whole units and keyed by metric name.
type marketbeatColumn struct {
	periodEnd string // YYYY-MM-DD
	values    map[string]float64
}

// fetchMarketbeatCashFlow returns one issuer's cash flow columns, newest
// first, for the requested period.
func (c *callCtx) fetchMarketbeatCashFlow(symbol, period string) ([]marketbeatColumn, error) {
	run, err := c.run(marketbeat, marketbeatStatementsEndpoint, nil, map[string]any{"ticker": symbol})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Statements map[string][]map[string]any `json:"statements"`
		} `json:"data"`
	}
	if err := json.Unmarshal(run.Output, &payload); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "marketbeat statements payload must be an object"}
	}
	if payload.Status != "success" {
		return nil, &providers.SchemaDriftError{Msg: "marketbeat statements status must be 'success'"}
	}

	// Table names embed the company ("Annual Cash Flow Statements for
	// Apple"), so they are matched by prefix rather than by equality.
	wantPrefix := marketbeatCashFlowAnnualPrefix
	if period == "quarterly" {
		wantPrefix = marketbeatCashFlowQuarterly
	}
	var rows []map[string]any
	for name, table := range payload.Data.Statements {
		if strings.HasPrefix(name, wantPrefix) {
			rows = table
			break
		}
	}
	if rows == nil {
		return nil, &providers.SchemaDriftError{
			Msg: "marketbeat returned no " + strings.ToLower(wantPrefix) + " table for " + symbol}
	}
	return marketbeatColumns(rows)
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

	// Column keys are the fiscal year labels ("2025"). Sorting them
	// descending puts the newest period first, matching every other list
	// this server returns.
	labels := make([]string, 0, len(periodRow))
	for key := range periodRow {
		if _, err := strconv.Atoi(key); err == nil {
			labels = append(labels, key)
		}
	}
	if len(labels) == 0 {
		return nil, &providers.SchemaDriftError{Msg: "marketbeat cash flow table carried no period columns"}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(labels)))

	columns := make([]marketbeatColumn, 0, len(labels))
	for _, label := range labels {
		end := marketbeatDate(periodRow[label])
		if end == "" {
			continue // a column with no period end cannot be dated, so it is skipped
		}
		values := make(map[string]float64, len(byMetric))
		for metric, row := range byMetric {
			if metric == marketbeatPeriodEndMetric {
				continue
			}
			if v, ok := marketbeatNumber(row[label]); ok {
				values[metric] = v
			}
		}
		columns = append(columns, marketbeatColumn{periodEnd: end, values: values})
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

// marketbeatDate converts marketbeat's M/D/YYYY period end to the
// YYYY-MM-DD every record in this package uses.
func marketbeatDate(raw any) string {
	text, ok := raw.(string)
	if !ok {
		return ""
	}
	parts := strings.Split(strings.TrimSpace(text), "/")
	if len(parts) != 3 {
		return ""
	}
	month, err1 := strconv.Atoi(parts[0])
	day, err2 := strconv.Atoi(parts[1])
	year, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil ||
		month < 1 || month > 12 || day < 1 || day > 31 || year < 1900 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
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
	m := marketbeatCashFlowMetrics
	reportPeriod := col.periodEnd
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
		if v, ok := col.values[metric]; ok {
			v = -v
			return &v
		}
		return nil
	}

	rec.NetIncome = get(m.netIncome)
	rec.DepreciationAndAmortization = get(m.depreciation)
	rec.NetCashFlowFromOperations = get(m.operating)
	rec.CapitalExpenditure = negated(m.capex)
	rec.NetCashFlowFromInvesting = get(m.investing)
	rec.DividendsAndOtherCashDistributions = negated(m.dividends)
	rec.NetCashFlowFromFinancing = get(m.financing)
	rec.ChangeInCashAndEquivalents = get(m.change)
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
	var cashErr error
	var filingsRun *monid.Run
	var filingsErr error
	concurrent2(
		func() { columns, cashErr = c.fetchMarketbeatCashFlow(parsed.ticker, parsed.period) },
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

	records := make([]any, 0, len(columns))
	for _, col := range columns {
		day, perr := time.Parse(dateLayout, col.periodEnd)
		if perr != nil {
			continue
		}
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
		fiscalPeriod := providers.FiscalPeriodLabel(row, marketbeatFiscalEndMonth(columns), parsed.period == "annual")
		records = append(records, buildMarketbeatCashFlow(parsed.ticker, parsed.period, col, fiscalPeriod, identity))
		if len(records) == parsed.limit {
			break
		}
	}
	return Result{Value: records, WrapperKey: "cash_flow_statements", Paginate: true}, nil
}

// marketbeatFiscalEndMonth reads the company's fiscal year end from its
// own reported period ends, which is what the quarterly fiscal label
// counts from. Nil when no column carries a usable date, in which case
// the label is omitted rather than counted from January.
func marketbeatFiscalEndMonth(columns []marketbeatColumn) *int {
	for _, col := range columns {
		if day, err := time.Parse(dateLayout, col.periodEnd); err == nil {
			month := int(day.Month())
			return &month
		}
	}
	return nil
}
