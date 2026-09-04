package providers

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// MetricsPeriod is the financial-metrics reporting period.
// Ported from financial_metrics.MetricsPeriod.
type MetricsPeriod string

const (
	PeriodAnnual    MetricsPeriod = "annual"
	PeriodQuarterly MetricsPeriod = "quarterly"
	PeriodTTM       MetricsPeriod = "ttm"
)

// Statement namespaces for the joined-row field keys below, mirroring
// financial_metrics.py's own _top/_child helpers and _INCOME/_BALANCE/_CASH
// constants. This parser is independent of statements.go's: financial
// metrics has its own joined-row parser across the three statements.
const (
	metricsIncome  = "income"
	metricsBalance = "balance"
	metricsCash    = "cash"
)

func metricsTopField(statement, label string) string { return statement + "|top|" + label }
func metricsChildField(statement, parent, label string) string {
	return statement + "|" + parent + "|" + label
}

// Field keys, ported one-for-one from financial_metrics.py's module-level
// REVENUE, COST_OF_REVENUE, ... constants.
var (
	fieldRevenue         = metricsTopField(metricsIncome, "Revenue")
	fieldCostOfRevenue   = metricsTopField(metricsIncome, "Cost of Revenue")
	fieldGrossProfit     = metricsTopField(metricsIncome, "Gross Profit")
	fieldOperatingIncome = metricsTopField(metricsIncome, "Operating Income")
	fieldNetIncome       = metricsTopField(metricsIncome, "Net Income")
	fieldDilutedEPS      = metricsTopField(metricsIncome, "EPS (Diluted)")
	fieldEBIT            = metricsTopField(metricsIncome, "EBIT")
	fieldEBITDA          = metricsTopField(metricsIncome, "EBITDA")
	fieldInterestExpense = metricsChildField(metricsIncome, "Non-Operating Items", "Non-Operating Interest Expense")

	fieldCurrentAssets      = metricsTopField(metricsBalance, "Total Current Assets")
	fieldCurrentLiabilities = metricsTopField(metricsBalance, "Total Current Liabilities")
	fieldTotalAssets        = metricsTopField(metricsBalance, "Total Assets")
	fieldShareholdersEquity = metricsTopField(metricsBalance, "Total Shareholders Equity")
	fieldCashAndEquivalents = metricsChildField(metricsBalance, "Total Current Assets", "Cash and Cash Equivalents")
	fieldAccountsReceivable = metricsChildField(metricsBalance, "Total Current Assets", "Accounts Receivable")
	fieldInventory          = metricsChildField(metricsBalance, "Total Current Assets", "Inventory")
	fieldShortTermDebt      = metricsChildField(metricsBalance, "Total Current Liabilities", "Short-Term Debt")
	fieldLongTermDebt       = metricsChildField(metricsBalance, "Total Non-Current Liabilities", "Long-Term Debt")

	fieldOperatingCashFlow = metricsTopField(metricsCash, "Cash Flow from Operating Activities")
	fieldFreeCashFlow      = metricsTopField(metricsCash, "Free Cash Flow")
	fieldSharesOutstanding = metricsTopField(metricsIncome, "Shares Outstanding (Basic)")
	fieldCommonDividends   = metricsChildField(metricsCash, "Cash Flow from Financing Activities", "Common Dividends")
)

var metricsFlowFields = []string{
	fieldRevenue, fieldCostOfRevenue, fieldGrossProfit, fieldOperatingIncome, fieldNetIncome,
	fieldDilutedEPS, fieldEBIT, fieldEBITDA, fieldInterestExpense, fieldOperatingCashFlow,
	fieldFreeCashFlow, fieldCommonDividends,
}

var metricsBalanceFields = []string{
	fieldCurrentAssets, fieldCurrentLiabilities, fieldTotalAssets, fieldShareholdersEquity,
	fieldCashAndEquivalents, fieldAccountsReceivable, fieldInventory, fieldShortTermDebt, fieldLongTermDebt,
}

type growthOutput struct{ name, field string }

// growthOutputs ports financial_metrics._GROWTH_OUTPUTS, in its dict order.
var growthOutputs = []growthOutput{
	{"revenue_growth", fieldRevenue},
	{"earnings_growth", fieldNetIncome},
	{"book_value_growth", fieldShareholdersEquity},
	{"earnings_per_share_growth", fieldDilutedEPS},
	{"free_cash_flow_growth", fieldFreeCashFlow},
	{"operating_income_growth", fieldOperatingIncome},
	{"ebitda_growth", fieldEBITDA},
}

// FinancialMetricsRecord is one Financial Datasets FinancialMetricsResponse
// record. Field order matches the Financial Datasets contract (captured 2026-09-04)
// FinancialMetricsResponse exactly (Go struct field order is JSON key
// order, so this is authoritative regardless of assignment order below).
// Valuation fields the validated routes cannot source without fabricating
// data (enterprise_value, price_to_* ratios, EV multiples,
// free_cash_flow_yield, peg_ratio, return_on_invested_capital, currency,
// filing_datetime) plus filing-identity fields not joined at this layer
// (accession_number, form_type, filing_url, filing_date) and pagination
// (next_page_url) are omitted, per docs/compatibility.md.
type FinancialMetricsRecord struct {
	Ticker                 *string  `json:"ticker,omitempty"`
	ReportPeriod           *string  `json:"report_period,omitempty"`
	FiscalPeriod           *string  `json:"fiscal_period,omitempty"`
	Period                 *string  `json:"period,omitempty"`
	GrossMargin            *float64 `json:"gross_margin,omitempty"`
	OperatingMargin        *float64 `json:"operating_margin,omitempty"`
	NetMargin              *float64 `json:"net_margin,omitempty"`
	ReturnOnEquity         *float64 `json:"return_on_equity,omitempty"`
	ReturnOnAssets         *float64 `json:"return_on_assets,omitempty"`
	AssetTurnover          *float64 `json:"asset_turnover,omitempty"`
	InventoryTurnover      *float64 `json:"inventory_turnover,omitempty"`
	ReceivablesTurnover    *float64 `json:"receivables_turnover,omitempty"`
	DaysSalesOutstanding   *float64 `json:"days_sales_outstanding,omitempty"`
	OperatingCycle         *float64 `json:"operating_cycle,omitempty"`
	WorkingCapitalTurnover *float64 `json:"working_capital_turnover,omitempty"`
	CurrentRatio           *float64 `json:"current_ratio,omitempty"`
	QuickRatio             *float64 `json:"quick_ratio,omitempty"`
	CashRatio              *float64 `json:"cash_ratio,omitempty"`
	OperatingCashFlowRatio *float64 `json:"operating_cash_flow_ratio,omitempty"`
	DebtToEquity           *float64 `json:"debt_to_equity,omitempty"`
	DebtToAssets           *float64 `json:"debt_to_assets,omitempty"`
	InterestCoverage       *float64 `json:"interest_coverage,omitempty"`
	RevenueGrowth          *float64 `json:"revenue_growth,omitempty"`
	EarningsGrowth         *float64 `json:"earnings_growth,omitempty"`
	BookValueGrowth        *float64 `json:"book_value_growth,omitempty"`
	EarningsPerShareGrowth *float64 `json:"earnings_per_share_growth,omitempty"`
	FreeCashFlowGrowth     *float64 `json:"free_cash_flow_growth,omitempty"`
	OperatingIncomeGrowth  *float64 `json:"operating_income_growth,omitempty"`
	EBITDAGrowth           *float64 `json:"ebitda_growth,omitempty"`
	PayoutRatio            *float64 `json:"payout_ratio,omitempty"`
	EarningsPerShare       *float64 `json:"earnings_per_share,omitempty"`
	BookValuePerShare      *float64 `json:"book_value_per_share,omitempty"`
	FreeCashFlowPerShare   *float64 `json:"free_cash_flow_per_share,omitempty"`
}

// metricsRow is financial_metrics.py's own _PeriodRow: numeric-only values
// joined across the three statements for one report period.
type metricsRow struct {
	ReportPeriod time.Time
	Values       map[string]float64
}

// MetricsFilters are the Financial Datasets report_period comparison
// filters accepted by NormalizeFinancialMetrics.
type MetricsFilters struct {
	Exact *time.Time
	GTE   *time.Time
	LTE   *time.Time
	GT    *time.Time
	LT    *time.Time
}

// MetricsData is the result of NormalizeFinancialMetrics.
// Ported from financial_metrics.MetricsData.
type MetricsData struct {
	Records              []FinancialMetricsRecord
	AsOf                 *string
	IncompleteTTMWindows int
	FiscalEndMonth       *int
}

// NormalizeFinancialMetrics ports financial_metrics.normalize_financial_metrics.
func NormalizeFinancialMetrics(
	value any,
	ticker string,
	period MetricsPeriod,
	limit int,
	filters MetricsFilters,
) (MetricsData, error) {
	root, err := metricsStatementRoot(value)
	if err != nil {
		return MetricsData{}, err
	}
	annual, err := metricsJoinedRows(root, "annual")
	if err != nil {
		return MetricsData{}, err
	}
	quarterly, err := metricsJoinedRows(root, "quarterly")
	if err != nil {
		return MetricsData{}, err
	}
	fiscalEndMonth := metricsFiscalYearEndMonth(annual)

	var rawRows []FinancialMetricsRecord
	incomplete := 0
	switch period {
	case PeriodAnnual:
		rawRows, err = baseMetricRows(annual, ticker, period, true, fiscalEndMonth)
	case PeriodQuarterly:
		rawRows, err = baseMetricRows(quarterly, ticker, period, false, fiscalEndMonth)
	default:
		rawRows, incomplete, err = ttmMetricRows(quarterly, ticker, fiscalEndMonth)
	}
	if err != nil {
		return MetricsData{}, err
	}

	filtered := make([]FinancialMetricsRecord, 0, len(rawRows))
	for _, record := range rawRows {
		day, parseErr := time.Parse(dateLayout, *record.ReportPeriod)
		if parseErr != nil {
			continue
		}
		if metricsDateMatches(day, filters) {
			filtered = append(filtered, record)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		di, _ := time.Parse(dateLayout, *filtered[i].ReportPeriod)
		dj, _ := time.Parse(dateLayout, *filtered[j].ReportPeriod)
		return di.After(dj)
	})
	if limit >= 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}
	var asOf *string
	if len(filtered) > 0 {
		asOf = filtered[0].ReportPeriod
	}
	return MetricsData{
		Records:              filtered,
		AsOf:                 asOf,
		IncompleteTTMWindows: incomplete,
		FiscalEndMonth:       fiscalEndMonth,
	}, nil
}

func baseMetricRows(
	rows []metricsRow,
	ticker string,
	period MetricsPeriod,
	annual bool,
	fiscalEndMonth *int,
) ([]FinancialMetricsRecord, error) {
	var byKey map[int]metricsRow
	var previousKey, growthKey func(metricsRow) int
	var err error
	if annual {
		byKey, err = uniqueByYear(rows)
		previousKey = func(r metricsRow) int { return r.ReportPeriod.Year() - 1 }
		growthKey = previousKey
	} else {
		byKey, err = uniqueByQuarter(rows)
		previousKey = func(r metricsRow) int { return quarterOrdinal(r.ReportPeriod) - 1 }
		growthKey = func(r metricsRow) int { return quarterOrdinal(r.ReportPeriod) - 4 }
	}
	if err != nil {
		return nil, err
	}

	result := make([]FinancialMetricsRecord, 0, len(rows))
	for _, row := range rows {
		var previous, growthPrior *metricsRow
		if p, ok := byKey[previousKey(row)]; ok {
			previous = &p
		}
		if g, ok := byKey[growthKey(row)]; ok {
			growthPrior = &g
		}
		fiscal := fiscalPeriodLabel(row.ReportPeriod, fiscalEndMonth, annual)
		result = append(result, metricRecord(ticker, period, row, previous, growthPrior, fiscal))
	}
	return result, nil
}

func ttmMetricRows(quarterly []metricsRow, ticker string, fiscalEndMonth *int) ([]FinancialMetricsRecord, int, error) {
	byQuarter, err := uniqueByQuarter(quarterly)
	if err != nil {
		return nil, 0, err
	}
	if len(byQuarter) == 0 {
		return nil, 0, nil
	}
	ordinals := make([]int, 0, len(byQuarter))
	for k := range byQuarter {
		ordinals = append(ordinals, k)
	}
	sort.Ints(ordinals)
	first := ordinals[0]

	ttmRows := map[int]metricsRow{}
	incomplete := 0
	for _, ordinal := range ordinals {
		if ordinal < first+3 {
			continue
		}
		window := make([]metricsRow, 4)
		complete := true
		for k := 0; k < 4; k++ {
			row, ok := byQuarter[ordinal-3+k]
			if !ok {
				complete = false
				break
			}
			window[k] = row
		}
		if !complete {
			incomplete++
			continue
		}
		ending := window[3]
		values := map[string]float64{}
		for _, field := range metricsFlowFields {
			sum := 0.0
			allPresent := true
			for _, w := range window {
				v, ok := w.Values[field]
				if !ok {
					allPresent = false
					break
				}
				sum += v
			}
			if allPresent {
				values[field] = sum
			}
		}
		for _, field := range metricsBalanceFields {
			if v, ok := ending.Values[field]; ok {
				values[field] = v
			}
		}
		ttmRows[ordinal] = metricsRow{ReportPeriod: ending.ReportPeriod, Values: values}
	}

	resultOrdinals := make([]int, 0, len(ttmRows))
	for k := range ttmRows {
		resultOrdinals = append(resultOrdinals, k)
	}
	sort.Ints(resultOrdinals)

	result := make([]FinancialMetricsRecord, 0, len(resultOrdinals))
	for _, ordinal := range resultOrdinals {
		row := ttmRows[ordinal]
		var previous, growthPrior *metricsRow
		if p, ok := byQuarter[ordinal-1]; ok {
			previous = &p
		}
		if g, ok := ttmRows[ordinal-4]; ok {
			growthPrior = &g
		}
		result = append(result, metricRecord(ticker, PeriodTTM, row, previous, growthPrior, nil))
	}
	return result, incomplete, nil
}

// metricRecord ports financial_metrics._metric_record. Field order in the
// output is fixed by the FinancialMetricsRecord struct, not by the order
// fields are assigned here.
func metricRecord(
	ticker string,
	period MetricsPeriod,
	row metricsRow,
	previous, growthPrior *metricsRow,
	fiscalPeriod *string,
) FinancialMetricsRecord {
	values := row.Values
	rec := FinancialMetricsRecord{}
	t := ticker
	rec.Ticker = &t
	rp := row.ReportPeriod.Format(dateLayout)
	rec.ReportPeriod = &rp
	rec.FiscalPeriod = fiscalPeriod
	p := string(period)
	rec.Period = &p

	rec.GrossMargin = ratioPtr(getPtr(values, fieldGrossProfit), getPtr(values, fieldRevenue))
	rec.OperatingMargin = ratioPtr(getPtr(values, fieldOperatingIncome), getPtr(values, fieldRevenue))
	rec.NetMargin = ratioPtr(getPtr(values, fieldNetIncome), getPtr(values, fieldRevenue))
	rec.ReturnOnEquity = ratioPtr(getPtr(values, fieldNetIncome), getPtr(values, fieldShareholdersEquity))
	rec.ReturnOnAssets = ratioPtr(getPtr(values, fieldNetIncome), getPtr(values, fieldTotalAssets))

	if previous != nil {
		prevValues := previous.Values
		rec.AssetTurnover = turnoverPtr(getPtr(values, fieldRevenue), getPtr(values, fieldTotalAssets), getPtr(prevValues, fieldTotalAssets))
		rec.InventoryTurnover = turnoverPtr(getPtr(values, fieldCostOfRevenue), getPtr(values, fieldInventory), getPtr(prevValues, fieldInventory))
		rec.ReceivablesTurnover = turnoverPtr(getPtr(values, fieldRevenue), getPtr(values, fieldAccountsReceivable), getPtr(prevValues, fieldAccountsReceivable))
		avgReceivables := averagePtr(getPtr(values, fieldAccountsReceivable), getPtr(prevValues, fieldAccountsReceivable))
		rec.DaysSalesOutstanding = daysOutstandingPtr(getPtr(values, fieldRevenue), avgReceivables)
		avgInventory := averagePtr(getPtr(values, fieldInventory), getPtr(prevValues, fieldInventory))
		inventoryTurnover := ratioPtr(getPtr(values, fieldCostOfRevenue), avgInventory)
		receivablesTurnover := ratioPtr(getPtr(values, fieldRevenue), avgReceivables)
		if inventoryTurnover != nil && receivablesTurnover != nil {
			v := *inventoryTurnover + *receivablesTurnover
			rec.OperatingCycle = &v
		}
		currentWorkingCapital := differencePtr(getPtr(values, fieldCurrentAssets), getPtr(values, fieldCurrentLiabilities))
		previousWorkingCapital := differencePtr(getPtr(prevValues, fieldCurrentAssets), getPtr(prevValues, fieldCurrentLiabilities))
		rec.WorkingCapitalTurnover = turnoverPtr(getPtr(values, fieldRevenue), currentWorkingCapital, previousWorkingCapital)
	}

	rec.CurrentRatio = ratioPtr(getPtr(values, fieldCurrentAssets), getPtr(values, fieldCurrentLiabilities))
	quickAssets := differencePtr(getPtr(values, fieldCurrentAssets), getPtr(values, fieldInventory))
	rec.QuickRatio = ratioPtr(quickAssets, getPtr(values, fieldCurrentLiabilities))
	rec.CashRatio = ratioPtr(getPtr(values, fieldCashAndEquivalents), getPtr(values, fieldCurrentLiabilities))
	rec.OperatingCashFlowRatio = ratioPtr(getPtr(values, fieldOperatingCashFlow), getPtr(values, fieldCurrentLiabilities))

	shortDebt := getPtr(values, fieldShortTermDebt)
	longDebt := getPtr(values, fieldLongTermDebt)
	var totalDebt *float64
	if shortDebt != nil && longDebt != nil {
		v := *shortDebt + *longDebt
		totalDebt = &v
	}
	rec.DebtToEquity = ratioPtr(totalDebt, getPtr(values, fieldShareholdersEquity))
	rec.DebtToAssets = ratioPtr(totalDebt, getPtr(values, fieldTotalAssets))
	rec.InterestCoverage = ratioPtr(getPtr(values, fieldEBIT), getPtr(values, fieldInterestExpense))

	commonDividends := getPtr(values, fieldCommonDividends)
	var absDividends *float64
	if commonDividends != nil {
		v := math.Abs(*commonDividends)
		absDividends = &v
	}
	rec.PayoutRatio = ratioPtr(absDividends, getPtr(values, fieldNetIncome))

	if growthPrior != nil {
		gv := growthPrior.Values
		for _, g := range growthOutputs {
			out := growthRatioPtr(getPtr(values, g.field), getPtr(gv, g.field))
			switch g.name {
			case "revenue_growth":
				rec.RevenueGrowth = out
			case "earnings_growth":
				rec.EarningsGrowth = out
			case "book_value_growth":
				rec.BookValueGrowth = out
			case "earnings_per_share_growth":
				rec.EarningsPerShareGrowth = out
			case "free_cash_flow_growth":
				rec.FreeCashFlowGrowth = out
			case "operating_income_growth":
				rec.OperatingIncomeGrowth = out
			case "ebitda_growth":
				rec.EBITDAGrowth = out
			}
		}
	}

	if eps := getPtr(values, fieldDilutedEPS); eps != nil {
		rec.EarningsPerShare = eps
	}
	rec.BookValuePerShare = ratioPtr(getPtr(values, fieldShareholdersEquity), getPtr(values, fieldSharesOutstanding))
	rec.FreeCashFlowPerShare = ratioPtr(getPtr(values, fieldFreeCashFlow), getPtr(values, fieldSharesOutstanding))

	return rec
}

func metricsJoinedRows(root map[string]any, period string) ([]metricsRow, error) {
	incomeSection, err := metricsRequiredSection(root, "incomeStatement")
	if err != nil {
		return nil, err
	}
	balanceSection, err := metricsRequiredSection(root, "balanceSheet")
	if err != nil {
		return nil, err
	}
	cashSection, err := metricsRequiredSection(root, "cashflow")
	if err != nil {
		return nil, err
	}
	income, err := metricsSectionRows(incomeSection, metricsIncome, period)
	if err != nil {
		return nil, err
	}
	balance, err := metricsSectionRows(balanceSection, metricsBalance, period)
	if err != nil {
		return nil, err
	}
	cash, err := metricsSectionRows(cashSection, metricsCash, period)
	if err != nil {
		return nil, err
	}

	dates := make([]time.Time, 0, len(income))
	for day := range income {
		if _, ok := balance[day]; !ok {
			continue
		}
		if _, ok := cash[day]; !ok {
			continue
		}
		dates = append(dates, day)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })

	rows := make([]metricsRow, 0, len(dates))
	for _, day := range dates {
		merged := map[string]float64{}
		for k, v := range income[day] {
			merged[k] = v
		}
		for k, v := range balance[day] {
			merged[k] = v
		}
		for k, v := range cash[day] {
			merged[k] = v
		}
		rows = append(rows, metricsRow{ReportPeriod: day, Values: merged})
	}
	return rows, nil
}

func metricsSectionRows(section map[string]any, statement, period string) (map[time.Time]map[string]float64, error) {
	labels, err := metricsLabels(section["labels"], fmt.Sprintf("DefiLlama %s labels", statement))
	if err != nil {
		return nil, err
	}
	childrenValue, _ := section["children"].(map[string]any)
	var childDefinitions map[string]any
	if childrenValue != nil {
		childDefinitions, _ = childrenValue[period].(map[string]any)
	}
	block, err := metricsObject(section[period], fmt.Sprintf("DefiLlama %s.%s", statement, period))
	if err != nil {
		return nil, err
	}
	dates, err := metricsDates(block["periodEnding"], fmt.Sprintf("DefiLlama %s.%s.periodEnding", statement, period))
	if err != nil {
		return nil, err
	}
	values, err := metricsValueRows(block["values"], len(labels), len(dates), fmt.Sprintf("DefiLlama %s.%s.values", statement, period))
	if err != nil {
		return nil, err
	}

	rows := make(map[time.Time]map[string]float64, len(dates))
	for _, day := range dates {
		rows[day] = map[string]float64{}
	}
	for rowIndex, label := range labels {
		key := metricsTopField(statement, label)
		for column, day := range dates {
			if operand := values[rowIndex][column]; operand != nil {
				rows[day][key] = *operand
			}
		}
	}

	blockChildren, _ := block["children"].(map[string]any)
	if !sameKeySet(blockChildren, childDefinitions) {
		return nil, schemaDriftf("DefiLlama %s.%s child definitions and values differ", statement, period)
	}
	for parent, definitionValue := range childDefinitions {
		definition, err := metricsObject(definitionValue, fmt.Sprintf("DefiLlama %s.%s.%s definition", statement, period, parent))
		if err != nil {
			return nil, err
		}
		childLabels, err := metricsLabels(definition["labels"], fmt.Sprintf("DefiLlama %s.%s.%s labels", statement, period, parent))
		if err != nil {
			return nil, err
		}
		childBlock, err := metricsObject(blockChildren[parent], fmt.Sprintf("DefiLlama %s.%s.%s", statement, period, parent))
		if err != nil {
			return nil, err
		}
		childValues, err := metricsValueRows(childBlock["values"], len(childLabels), len(dates), fmt.Sprintf("DefiLlama %s.%s.%s.values", statement, period, parent))
		if err != nil {
			return nil, err
		}
		for rowIndex, label := range childLabels {
			key := metricsChildField(statement, parent, label)
			for column, day := range dates {
				if operand := childValues[rowIndex][column]; operand != nil {
					rows[day][key] = *operand
				}
			}
		}
	}
	return rows, nil
}

func sameKeySet(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func metricsStatementRoot(value any) (map[string]any, error) {
	root, err := metricsObject(value, "DefiLlama statements payload")
	if err != nil {
		return nil, err
	}
	if _, hasIncome := root["incomeStatement"]; !hasIncome {
		if data, ok := root["data"].(map[string]any); ok {
			return data, nil
		}
		if _, hasData := root["data"]; hasData {
			return nil, schemaDriftf("DefiLlama statements data must be an object")
		}
	}
	return root, nil
}

func metricsRequiredSection(root map[string]any, key string) (map[string]any, error) {
	return metricsObject(root[key], fmt.Sprintf("DefiLlama statements %s", key))
}

func metricsObject(value any, name string) (map[string]any, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, schemaDriftf("%s must be an object", name)
	}
	return obj, nil
}

func metricsLabels(value any, name string) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, schemaDriftf("%s must be an array", name)
	}
	result := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for index, item := range items {
		s, ok := item.(string)
		if !ok || s == "" {
			return nil, schemaDriftf("%s[%d] must be a non-empty string", name, index)
		}
		if _, dup := seen[s]; dup {
			return nil, schemaDriftf("%s contains ambiguous duplicate labels", name)
		}
		seen[s] = struct{}{}
		result = append(result, s)
	}
	return result, nil
}

func metricsDates(value any, name string) ([]time.Time, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, schemaDriftf("%s must be an array", name)
	}
	result := make([]time.Time, 0, len(items))
	seen := map[time.Time]struct{}{}
	for index, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, schemaDriftf("%s[%d] must be an ISO date string", name, index)
		}
		day, err := time.Parse(dateLayout, s)
		if err != nil || day.Format(dateLayout) != s {
			return nil, schemaDriftf("%s[%d] must use YYYY-MM-DD", name, index)
		}
		if _, dup := seen[day]; dup {
			return nil, schemaDriftf("%s contains duplicate periods", name)
		}
		seen[day] = struct{}{}
		result = append(result, day)
	}
	return result, nil
}

func metricsValueRows(value any, rowCount, columnCount int, name string) ([][]*float64, error) {
	items, ok := value.([]any)
	if !ok || len(items) != rowCount {
		return nil, schemaDriftf("%s has the wrong row count", name)
	}
	rows := make([][]*float64, len(items))
	for rowIndex, rawRow := range items {
		row, ok := rawRow.([]any)
		if !ok || len(row) != columnCount {
			return nil, schemaDriftf("%s[%d] has the wrong width", name, rowIndex)
		}
		parsed := make([]*float64, len(row))
		for column, operand := range row {
			if operand == nil {
				continue
			}
			f, ok := operand.(float64)
			if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, schemaDriftf("%s[%d][%d] must be finite numeric data or null", name, rowIndex, column)
			}
			v := f
			parsed[column] = &v
		}
		rows[rowIndex] = parsed
	}
	return rows, nil
}

func uniqueByYear(rows []metricsRow) (map[int]metricsRow, error) {
	result := make(map[int]metricsRow, len(rows))
	for _, row := range rows {
		year := row.ReportPeriod.Year()
		if _, dup := result[year]; dup {
			return nil, schemaDriftf("DefiLlama annual statements contain two periods in one year")
		}
		result[year] = row
	}
	return result, nil
}

func uniqueByQuarter(rows []metricsRow) (map[int]metricsRow, error) {
	result := make(map[int]metricsRow, len(rows))
	for _, row := range rows {
		ordinal := quarterOrdinal(row.ReportPeriod)
		if _, dup := result[ordinal]; dup {
			return nil, schemaDriftf("DefiLlama quarterly statements contain two periods in one quarter")
		}
		result[ordinal] = row
	}
	return result, nil
}

func metricsFiscalYearEndMonth(annual []metricsRow) *int {
	months := map[int]struct{}{}
	for _, row := range annual {
		months[int(row.ReportPeriod.Month())] = struct{}{}
	}
	if len(months) != 1 {
		return nil
	}
	for m := range months {
		v := m
		return &v
	}
	return nil
}

func metricsDateMatches(value time.Time, filters MetricsFilters) bool {
	if filters.Exact != nil && !value.Equal(*filters.Exact) {
		return false
	}
	if filters.GTE != nil && value.Before(*filters.GTE) {
		return false
	}
	if filters.LTE != nil && value.After(*filters.LTE) {
		return false
	}
	if filters.GT != nil && !value.After(*filters.GT) {
		return false
	}
	if filters.LT != nil && !value.Before(*filters.LT) {
		return false
	}
	return true
}

// getPtr reads a numeric value from a joined-row map, mirroring Python's
// values.get(field) returning None when absent.
func getPtr(values map[string]float64, key string) *float64 {
	if v, ok := values[key]; ok {
		c := v
		return &c
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// ratioPtr ports financial_metrics._ratio.
func ratioPtr(numerator, denominator *float64) *float64 {
	if numerator == nil || denominator == nil || *denominator == 0 {
		return nil
	}
	v := *numerator / *denominator
	if !finite(v) {
		return nil
	}
	return &v
}

// turnoverPtr ports financial_metrics._turnover.
func turnoverPtr(numerator, current, previous *float64) *float64 {
	if numerator == nil || current == nil || previous == nil {
		return nil
	}
	average := (*current + *previous) / 2
	if average == 0 {
		return nil
	}
	v := *numerator / average
	if !finite(v) {
		return nil
	}
	return &v
}

// growthRatioPtr ports financial_metrics._growth.
func growthRatioPtr(current, previous *float64) *float64 {
	if current == nil || previous == nil || *previous == 0 {
		return nil
	}
	v := (*current - *previous) / math.Abs(*previous)
	if !finite(v) {
		return nil
	}
	return &v
}

// differencePtr ports financial_metrics._difference.
func differencePtr(minuend, subtrahend *float64) *float64 {
	if minuend == nil || subtrahend == nil {
		return nil
	}
	v := *minuend - *subtrahend
	return &v
}

// averagePtr ports financial_metrics._average.
func averagePtr(current, previous *float64) *float64 {
	if current == nil || previous == nil {
		return nil
	}
	v := (*current + *previous) / 2
	return &v
}

// daysOutstandingPtr ports financial_metrics._days.
func daysOutstandingPtr(revenue, averageReceivables *float64) *float64 {
	if revenue == nil || averageReceivables == nil || *revenue == 0 {
		return nil
	}
	v := 365 * (*averageReceivables) / (*revenue)
	if !finite(v) {
		return nil
	}
	return &v
}
