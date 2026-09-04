package providers

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
)

// earningsForms are the filing forms earnings composition accepts.
// Ported from earnings.EARNINGS_FORMS.
var earningsForms = map[string]struct{}{"10-K": {}, "10-Q": {}}

// marginBase pairs an EarningsTimeDimension margin field name with the
// income-statement label used as its numerator. Ported from
// earnings._MARGIN_BASES.
type marginBase struct{ name, label string }

var earningsMarginBases = []marginBase{
	{"gross_margin", "Gross Profit"},
	{"operating_margin", "Operating Income"},
	{"net_margin", "Net Income"},
}

// RawFiling is one filings-index row in the shape earnings.py's
// _earnings_filings consumes: normalize_filings' snake_case intermediate
// shape (filing_date, report_date, form, primary_document_url), not the
// raw camelCase DefiLlama payload. normalize_filings itself lives in
// monid_finance_mcp/normalize.py and is out of scope for this port; a
// caller resolves raw DefiLlama /equities/v1/filings rows into this shape
// before calling NormalizeEarnings.
type RawFiling struct {
	FilingDate         string
	ReportDate         string
	Form               string
	PrimaryDocumentURL string
}

// EarningsFiling is one filing event selected for earnings composition.
// Ported from earnings.EarningsFiling.
type EarningsFiling struct {
	ReportPeriod    time.Time
	FilingDate      time.Time
	Form            string
	FilingURL       string
	AccessionNumber string
}

// EarningsData is the result of NormalizeEarnings.
// Ported from earnings.EarningsData.
type EarningsData struct {
	Records        []fd.EarningsRecord
	FiscalEndMonth *int
}

// EarningsTimeDimension is one Financial Datasets EarningsTimeDimension
// block. Field order matches docs/fd-contract-reference.json
// EarningsTimeDimension exactly. Note: earnings.py's own _time_dimension
// builds its dict with all three margin fields (gross/operating/net)
// grouped together right after gross_profit, before operating_income and
// net_income; the published FD contract interleaves them (operating_income
// then operating_margin, net_income then net_margin). This port follows
// the published contract order, since Go struct field order is
// authoritative for json.Marshal regardless of assignment order, and
// docs/compatibility.md commits this project to "keys ... in schema
// property order". The underlying values and omission rules are otherwise
// an exact port of earnings.py.
type EarningsTimeDimension struct {
	Revenue                         *float64 `json:"revenue,omitempty"`
	RevenueChg                      *float64 `json:"revenue_chg,omitempty"`
	RevenueYoYChg                   *float64 `json:"revenue_yoy_chg,omitempty"`
	EarningsPerShare                *float64 `json:"earnings_per_share,omitempty"`
	EarningsPerShareChg             *float64 `json:"earnings_per_share_chg,omitempty"`
	EarningsPerShareYoYChg          *float64 `json:"earnings_per_share_yoy_chg,omitempty"`
	GrossProfit                     *float64 `json:"gross_profit,omitempty"`
	GrossProfitChg                  *float64 `json:"gross_profit_chg,omitempty"`
	GrossProfitYoYChg               *float64 `json:"gross_profit_yoy_chg,omitempty"`
	GrossMargin                     *float64 `json:"gross_margin,omitempty"`
	GrossMarginChgBps               *float64 `json:"gross_margin_chg_bps,omitempty"`
	GrossMarginChgPct               *float64 `json:"gross_margin_chg_pct,omitempty"`
	GrossMarginYoYChgBps            *float64 `json:"gross_margin_yoy_chg_bps,omitempty"`
	GrossMarginYoYChgPct            *float64 `json:"gross_margin_yoy_chg_pct,omitempty"`
	OperatingIncome                 *float64 `json:"operating_income,omitempty"`
	OperatingIncomeChg              *float64 `json:"operating_income_chg,omitempty"`
	OperatingIncomeYoYChg           *float64 `json:"operating_income_yoy_chg,omitempty"`
	OperatingMargin                 *float64 `json:"operating_margin,omitempty"`
	OperatingMarginChgBps           *float64 `json:"operating_margin_chg_bps,omitempty"`
	OperatingMarginChgPct           *float64 `json:"operating_margin_chg_pct,omitempty"`
	OperatingMarginYoYChgBps        *float64 `json:"operating_margin_yoy_chg_bps,omitempty"`
	OperatingMarginYoYChgPct        *float64 `json:"operating_margin_yoy_chg_pct,omitempty"`
	NetIncome                       *float64 `json:"net_income,omitempty"`
	NetIncomeChg                    *float64 `json:"net_income_chg,omitempty"`
	NetIncomeYoYChg                 *float64 `json:"net_income_yoy_chg,omitempty"`
	NetMargin                       *float64 `json:"net_margin,omitempty"`
	NetMarginChgBps                 *float64 `json:"net_margin_chg_bps,omitempty"`
	NetMarginChgPct                 *float64 `json:"net_margin_chg_pct,omitempty"`
	NetMarginYoYChgBps              *float64 `json:"net_margin_yoy_chg_bps,omitempty"`
	NetMarginYoYChgPct              *float64 `json:"net_margin_yoy_chg_pct,omitempty"`
	WeightedAverageShares           *float64 `json:"weighted_average_shares,omitempty"`
	WeightedAverageSharesDiluted    *float64 `json:"weighted_average_shares_diluted,omitempty"`
	CashAndEquivalents              *float64 `json:"cash_and_equivalents,omitempty"`
	ChangeInCashAndEquivalents      *float64 `json:"change_in_cash_and_equivalents,omitempty"`
	TotalDebt                       *float64 `json:"total_debt,omitempty"`
	TotalAssets                     *float64 `json:"total_assets,omitempty"`
	TotalLiabilities                *float64 `json:"total_liabilities,omitempty"`
	ShareholdersEquity              *float64 `json:"shareholders_equity,omitempty"`
	NetCashFlowFromOperations       *float64 `json:"net_cash_flow_from_operations,omitempty"`
	NetCashFlowFromOperationsChg    *float64 `json:"net_cash_flow_from_operations_chg,omitempty"`
	NetCashFlowFromOperationsYoYChg *float64 `json:"net_cash_flow_from_operations_yoy_chg,omitempty"`
	NetCashFlowFromInvesting        *float64 `json:"net_cash_flow_from_investing,omitempty"`
	NetCashFlowFromInvestingChg     *float64 `json:"net_cash_flow_from_investing_chg,omitempty"`
	NetCashFlowFromInvestingYoYChg  *float64 `json:"net_cash_flow_from_investing_yoy_chg,omitempty"`
	NetCashFlowFromFinancing        *float64 `json:"net_cash_flow_from_financing,omitempty"`
	NetCashFlowFromFinancingChg     *float64 `json:"net_cash_flow_from_financing_chg,omitempty"`
	NetCashFlowFromFinancingYoYChg  *float64 `json:"net_cash_flow_from_financing_yoy_chg,omitempty"`
	CapitalExpenditure              *float64 `json:"capital_expenditure,omitempty"`
	CapitalExpenditureChg           *float64 `json:"capital_expenditure_chg,omitempty"`
	CapitalExpenditureYoYChg        *float64 `json:"capital_expenditure_yoy_chg,omitempty"`
	FreeCashFlow                    *float64 `json:"free_cash_flow,omitempty"`
	FreeCashFlowChg                 *float64 `json:"free_cash_flow_chg,omitempty"`
	FreeCashFlowYoYChg              *float64 `json:"free_cash_flow_yoy_chg,omitempty"`
}

// NormalizeEarnings composes EarningsRecord objects for 10-K and 10-Q
// filing events. Ported from earnings.normalize_earnings.
func NormalizeEarnings(statementsValue any, filingsRows []RawFiling, ticker string, limit int) (EarningsData, error) {
	income, err := ParseStatementSeries(statementsValue, "income")
	if err != nil {
		return EarningsData{}, err
	}
	balance, err := ParseStatementSeries(statementsValue, "balance")
	if err != nil {
		return EarningsData{}, err
	}
	cash, err := ParseStatementSeries(statementsValue, "cash")
	if err != nil {
		return EarningsData{}, err
	}
	quarterly := joinedByDate(income.Quarterly, balance.Quarterly, cash.Quarterly)
	annual := joinedByDate(income.Annual, balance.Annual, cash.Annual)
	fiscalEndMonth := fiscalYearEndMonthOfRows(income.Annual)

	filings, err := earningsFilings(filingsRows)
	if err != nil {
		return EarningsData{}, err
	}
	sort.SliceStable(filings, func(i, j int) bool {
		if !filings[i].FilingDate.Equal(filings[j].FilingDate) {
			return filings[i].FilingDate.After(filings[j].FilingDate)
		}
		return filings[i].ReportPeriod.After(filings[j].ReportPeriod)
	})

	var records []fd.EarningsRecord
	for _, filing := range filings {
		quarterValues, ok := quarterly[filing.ReportPeriod]
		if !ok {
			continue
		}
		record := fd.EarningsRecord{}
		tickerCopy := ticker
		record.Ticker = &tickerCopy
		reportPeriod := filing.ReportPeriod.Format(dateLayout)
		record.ReportPeriod = &reportPeriod
		record.FiscalPeriod = fiscalPeriodLabel(filing.ReportPeriod, fiscalEndMonth, false)
		form := filing.Form
		record.SourceType = &form
		filingDate := filing.FilingDate.Format(dateLayout)
		record.FilingDate = &filingDate
		filingURL := filing.FilingURL
		record.FilingURL = &filingURL
		accession := filing.AccessionNumber
		record.AccessionNumber = &accession

		quarterlyBlock := buildTimeDimension(
			quarterValues,
			previousQuarter(quarterly, filing.ReportPeriod),
			yearOverYearQuarter(quarterly, filing.ReportPeriod),
			true,
		)
		quarterlyJSON, marshalErr := json.Marshal(quarterlyBlock)
		if marshalErr != nil {
			return EarningsData{}, marshalErr
		}
		record.Quarterly = quarterlyJSON

		if filing.Form == "10-K" {
			if annualValues, ok := annual[filing.ReportPeriod]; ok {
				annualBlock := buildTimeDimension(
					annualValues,
					previousYear(annual, filing.ReportPeriod),
					nil,
					false,
				)
				annualJSON, marshalErr := json.Marshal(annualBlock)
				if marshalErr != nil {
					return EarningsData{}, marshalErr
				}
				record.Annual = annualJSON
			}
		}

		records = append(records, record)
		if len(records) >= limit {
			break
		}
	}
	return EarningsData{Records: records, FiscalEndMonth: fiscalEndMonth}, nil
}

func joinedByDate(seriesLists ...[]PeriodRow) map[time.Time]map[string]any {
	maps := make([]map[time.Time]map[string]any, len(seriesLists))
	for i, rows := range seriesLists {
		m := make(map[time.Time]map[string]any, len(rows))
		for _, row := range rows {
			m[row.ReportPeriod] = row.Values
		}
		maps[i] = m
	}
	joined := map[time.Time]map[string]any{}
	if len(maps) == 0 {
		return joined
	}
	for day := range maps[0] {
		inAll := true
		for _, m := range maps[1:] {
			if _, ok := m[day]; !ok {
				inAll = false
				break
			}
		}
		if !inAll {
			continue
		}
		merged := map[string]any{}
		for _, m := range maps {
			for k, v := range m[day] {
				merged[k] = v
			}
		}
		joined[day] = merged
	}
	return joined
}

func earningsFilings(rows []RawFiling) ([]EarningsFiling, error) {
	var filings []EarningsFiling
	for _, row := range rows {
		form := strings.TrimSpace(row.Form)
		if form == "" {
			continue
		}
		formUpper := strings.ToUpper(form)
		if _, ok := earningsForms[formUpper]; !ok {
			continue
		}
		reportPeriod, ok := optDate(row.ReportDate)
		if !ok {
			continue
		}
		filingDate, ok := optDate(row.FilingDate)
		if !ok {
			continue
		}
		if row.PrimaryDocumentURL == "" {
			continue
		}
		accession := ""
		if acc := DeriveAccession(row.PrimaryDocumentURL); acc != nil {
			accession = *acc
		}
		filings = append(filings, EarningsFiling{
			ReportPeriod:    reportPeriod,
			FilingDate:      filingDate,
			Form:            formUpper,
			FilingURL:       row.PrimaryDocumentURL,
			AccessionNumber: accession,
		})
	}
	if len(filings) == 0 {
		return nil, schemaDriftf("DefiLlama filings index has no 10-K or 10-Q rows")
	}
	return filings, nil
}

func optDate(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	s := value
	if len(s) > 10 {
		s = s[:10]
	}
	day, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// buildTimeDimension ports earnings._time_dimension; see the
// EarningsTimeDimension doc comment for the field-order note.
func buildTimeDimension(values, previous, yearOverYear map[string]any, quarterly bool) *EarningsTimeDimension {
	block := &EarningsTimeDimension{}
	block.Revenue, block.RevenueChg, block.RevenueYoYChg = putChange(values, previous, yearOverYear, "Revenue", quarterly)
	block.EarningsPerShare, block.EarningsPerShareChg, block.EarningsPerShareYoYChg = putChange(values, previous, yearOverYear, "EPS (Diluted)", quarterly)
	block.GrossProfit, block.GrossProfitChg, block.GrossProfitYoYChg = putChange(values, previous, yearOverYear, "Gross Profit", quarterly)
	block.OperatingIncome, block.OperatingIncomeChg, block.OperatingIncomeYoYChg = putChange(values, previous, yearOverYear, "Operating Income", quarterly)
	block.NetIncome, block.NetIncomeChg, block.NetIncomeYoYChg = putChange(values, previous, yearOverYear, "Net Income", quarterly)

	for _, base := range earningsMarginBases {
		margin, chgBps, chgPct, yoyBps, yoyPct := putMargin(values, previous, yearOverYear, base.label, quarterly)
		switch base.name {
		case "gross_margin":
			block.GrossMargin, block.GrossMarginChgBps, block.GrossMarginChgPct = margin, chgBps, chgPct
			block.GrossMarginYoYChgBps, block.GrossMarginYoYChgPct = yoyBps, yoyPct
		case "operating_margin":
			block.OperatingMargin, block.OperatingMarginChgBps, block.OperatingMarginChgPct = margin, chgBps, chgPct
			block.OperatingMarginYoYChgBps, block.OperatingMarginYoYChgPct = yoyBps, yoyPct
		case "net_margin":
			block.NetMargin, block.NetMarginChgBps, block.NetMarginChgPct = margin, chgBps, chgPct
			block.NetMarginYoYChgBps, block.NetMarginYoYChgPct = yoyBps, yoyPct
		}
	}

	block.WeightedAverageShares = putValue(values, "Shares Outstanding (Basic)")
	block.WeightedAverageSharesDiluted = putValue(values, "Shares Outstanding (Diluted)")
	block.CashAndEquivalents = putValue(values, "Total Current Assets|Cash and Cash Equivalents")
	block.ChangeInCashAndEquivalents = putValue(values, "Net Cash Flow")

	shortDebt, shortOK := numOf(values["Total Current Liabilities|Short-Term Debt"])
	longDebt, longOK := numOf(values["Total Non-Current Liabilities|Long-Term Debt"])
	if shortOK && longOK {
		total := shortDebt + longDebt
		block.TotalDebt = &total
	}
	block.TotalAssets = putValue(values, "Total Assets")
	block.TotalLiabilities = putValue(values, "Total Liabilities")
	block.ShareholdersEquity = putValue(values, "Total Shareholders Equity")

	block.NetCashFlowFromOperations, block.NetCashFlowFromOperationsChg, block.NetCashFlowFromOperationsYoYChg = putChange(values, previous, yearOverYear, "Cash Flow from Operating Activities", quarterly)
	block.NetCashFlowFromInvesting, block.NetCashFlowFromInvestingChg, block.NetCashFlowFromInvestingYoYChg = putChange(values, previous, yearOverYear, "Cash Flow from Investing Activities", quarterly)
	block.NetCashFlowFromFinancing, block.NetCashFlowFromFinancingChg, block.NetCashFlowFromFinancingYoYChg = putChange(values, previous, yearOverYear, "Cash Flow from Financing Activities", quarterly)
	block.CapitalExpenditure, block.CapitalExpenditureChg, block.CapitalExpenditureYoYChg = putChange(values, previous, yearOverYear, "Cash Flow from Investing Activities|Capital Expenditure", quarterly)
	block.FreeCashFlow, block.FreeCashFlowChg, block.FreeCashFlowYoYChg = putChange(values, previous, yearOverYear, "Free Cash Flow", quarterly)

	return block
}

// putChange ports earnings._put_change: current value plus decimal-ratio
// period-over-period and (quarterly-only) year-over-year change fields.
func putChange(values, previous, yearOverYear map[string]any, label string, quarterly bool) (cur, chg, yoyChg *float64) {
	current, ok := numOf(values[label])
	if !ok {
		return nil, nil, nil
	}
	cur = &current
	if previous != nil {
		if prior, ok := numOf(previous[label]); ok && prior != 0 {
			v := (current - prior) / math.Abs(prior)
			chg = &v
		}
	}
	if quarterly && yearOverYear != nil {
		if yoy, ok := numOf(yearOverYear[label]); ok && yoy != 0 {
			v := (current - yoy) / math.Abs(yoy)
			yoyChg = &v
		}
	}
	return cur, chg, yoyChg
}

// putMargin ports earnings._put_margin: margin plus absolute-bps and
// decimal-ratio period-over-period and (quarterly-only) year-over-year
// change fields.
func putMargin(values, previous, yearOverYear map[string]any, baseLabel string, quarterly bool) (margin, chgBps, chgPct, yoyBps, yoyPct *float64) {
	m, ok := marginOf(values, baseLabel)
	if !ok {
		return nil, nil, nil, nil, nil
	}
	margin = &m
	if priorMargin, ok := marginOf(previous, baseLabel); ok {
		bps := (m - priorMargin) * 10_000
		chgBps = &bps
		if priorMargin != 0 {
			pct := (m - priorMargin) / math.Abs(priorMargin)
			chgPct = &pct
		}
	}
	if quarterly {
		if yoyMargin, ok := marginOf(yearOverYear, baseLabel); ok {
			bps := (m - yoyMargin) * 10_000
			yoyBps = &bps
			if yoyMargin != 0 {
				pct := (m - yoyMargin) / math.Abs(yoyMargin)
				yoyPct = &pct
			}
		}
	}
	return margin, chgBps, chgPct, yoyBps, yoyPct
}

func marginOf(values map[string]any, baseLabel string) (float64, bool) {
	if values == nil {
		return 0, false
	}
	revenue, ok := numOf(values["Revenue"])
	if !ok || revenue == 0 {
		return 0, false
	}
	base, ok := numOf(values[baseLabel])
	if !ok {
		return 0, false
	}
	return base / revenue, true
}

func putValue(values map[string]any, label string) *float64 {
	if v, ok := numOf(values[label]); ok {
		return &v
	}
	return nil
}

func previousQuarter(quarterly map[time.Time]map[string]any, day time.Time) map[string]any {
	ordinal := quarterOrdinal(day)
	for candidate, values := range quarterly {
		if quarterOrdinal(candidate) == ordinal-1 {
			return values
		}
	}
	return nil
}

func yearOverYearQuarter(quarterly map[time.Time]map[string]any, day time.Time) map[string]any {
	ordinal := quarterOrdinal(day)
	for candidate, values := range quarterly {
		if quarterOrdinal(candidate) == ordinal-4 {
			return values
		}
	}
	return nil
}

func previousYear(annual map[time.Time]map[string]any, day time.Time) map[string]any {
	for candidate, values := range annual {
		if candidate.Year() == day.Year()-1 {
			return values
		}
	}
	return nil
}

// numOf mirrors earnings._num: only JSON numbers (never booleans) count.
func numOf(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	f, ok := value.(float64)
	return f, ok
}
