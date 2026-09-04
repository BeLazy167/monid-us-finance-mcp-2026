package providers

import (
	"encoding/json"
	"testing"
	"time"
)

// statementsFixtureJSON ports the STATEMENTS fixture from tests/test_service.py
// (income/balance/cashflow annual + quarterly matrices with one child block).
const statementsFixtureJSON = `{"incomeStatement":{"labels":["Revenue","Cost of Revenue","Gross Profit","Operating Income","Income Tax","Net Income","EPS (Basic)","EPS (Diluted)","Shares Outstanding (Basic)","EBIT"],"annual":{"periodEnding":["2024-12-31","2025-12-31"],"values":[[100,120],[60,70],[40,50],[30,35],[6,7],[24,28],[2.0,2.1],[1.9,2.0],[12.0,13.0],[31,36]],"children":{"Non-Operating Items":{"values":[[3,4]]}}},"quarterly":{"periodEnding":["2025-06-30","2025-09-30","2025-12-31","2026-03-31"],"values":[[20,22,24,25],[12,13,14,15],[8,9,10,10],[6,7,8,8],[1,1,2,2],[5,6,6,6],[0.5,0.6,0.5,0.5],[0.5,0.5,0.5,0.5],[10.0,10.0,11.0,12.0],[6,7,8,8]],"children":{"Non-Operating Items":{"values":[[1,1,2,2]]}}},"children":{"annual":{"Non-Operating Items":{"labels":["Non-Operating Interest Expense"]}},"quarterly":{"Non-Operating Items":{"labels":["Non-Operating Interest Expense"]}}}},"balanceSheet":{"labels":["Total Assets","Total Current Assets","Total Liabilities","Total Shareholders Equity"],"annual":{"periodEnding":["2024-12-31","2025-12-31"],"values":[[400,420],[200,210],[250,260],[150,160]]},"quarterly":{"periodEnding":["2025-06-30","2025-09-30","2025-12-31","2026-03-31"],"values":[[405,410,415,420],[202,206,208,210],[252,255,258,260],[153,155,157,160]]},"children":{}},"cashflow":{"labels":["Cash Flow from Operating Activities","Free Cash Flow","Net Cash Flow","End Cash Position","Net Income"],"annual":{"periodEnding":["2024-12-31","2025-12-31"],"values":[[60,70],[50,60],[10,11],[30,33],[24,28]]},"quarterly":{"periodEnding":["2025-06-30","2025-09-30","2025-12-31","2026-03-31"],"values":[[14,16,18,19],[12,13,15,16],[2,3,3,4],[31,32,33,33],[5,6,6,6]]},"children":{}}}`

func parseFixture(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("invalid fixture JSON: %v", err)
	}
	return value
}

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		t.Fatalf("invalid date %q: %v", s, err)
	}
	return d
}

func rowFor(t *testing.T, rows []PeriodRow, day string) PeriodRow {
	t.Helper()
	target := mustDate(t, day)
	for _, row := range rows {
		if row.ReportPeriod.Equal(target) {
			return row
		}
	}
	t.Fatalf("no row for %s", day)
	return PeriodRow{}
}

func TestParseStatementSeries_IncomeAnnual(t *testing.T) {
	value := parseFixture(t, statementsFixtureJSON)
	series, err := ParseStatementSeries(value, "income")
	if err != nil {
		t.Fatalf("ParseStatementSeries: %v", err)
	}
	if len(series.Annual) != 2 {
		t.Fatalf("want 2 annual rows, got %d", len(series.Annual))
	}
	newest := rowFor(t, series.Annual, "2025-12-31")
	cases := map[string]float64{
		"Revenue":                    120,
		"Cost of Revenue":            70,
		"Gross Profit":               50,
		"Operating Income":           35,
		"Income Tax":                 7,
		"Net Income":                 28,
		"EPS (Diluted)":              2.0,
		"Shares Outstanding (Basic)": 13.0,
		"EBIT":                       36,
		"Non-Operating Items|Non-Operating Interest Expense": 4,
	}
	for label, want := range cases {
		got, ok := asFloat(newest.Values[label])
		if !ok {
			t.Fatalf("missing label %q in %+v", label, newest.Values)
		}
		if got != want {
			t.Errorf("%s = %v, want %v", label, got, want)
		}
	}
}

func TestParseStatementSeries_BalanceAndCash(t *testing.T) {
	value := parseFixture(t, statementsFixtureJSON)

	balance, err := ParseStatementSeries(value, "balance")
	if err != nil {
		t.Fatalf("ParseStatementSeries(balance): %v", err)
	}
	newestBalance := rowFor(t, balance.Annual, "2025-12-31")
	if got, _ := asFloat(newestBalance.Values["Total Assets"]); got != 420 {
		t.Errorf("Total Assets = %v, want 420", got)
	}
	if got, _ := asFloat(newestBalance.Values["Total Current Assets"]); got != 210 {
		t.Errorf("Total Current Assets = %v, want 210", got)
	}
	if got, _ := asFloat(newestBalance.Values["Total Shareholders Equity"]); got != 160 {
		t.Errorf("Total Shareholders Equity = %v, want 160", got)
	}

	cash, err := ParseStatementSeries(value, "cash")
	if err != nil {
		t.Fatalf("ParseStatementSeries(cash): %v", err)
	}
	newestCash := rowFor(t, cash.Annual, "2025-12-31")
	if got, _ := asFloat(newestCash.Values["Cash Flow from Operating Activities"]); got != 70 {
		t.Errorf("CFO = %v, want 70", got)
	}
	if got, _ := asFloat(newestCash.Values["Free Cash Flow"]); got != 60 {
		t.Errorf("FCF = %v, want 60", got)
	}
	if got, _ := asFloat(newestCash.Values["End Cash Position"]); got != 33 {
		t.Errorf("End Cash Position = %v, want 33", got)
	}
}

func TestFiscalPeriodLabel_AnnualAndQuarterlyYearFirst(t *testing.T) {
	value := parseFixture(t, statementsFixtureJSON)
	series, err := ParseStatementSeries(value, "income")
	if err != nil {
		t.Fatalf("ParseStatementSeries: %v", err)
	}
	yearEndMonth := FiscalYearEndMonth(series)
	if yearEndMonth == nil || *yearEndMonth != 12 {
		t.Fatalf("FiscalYearEndMonth = %v, want 12", yearEndMonth)
	}

	annualRow := rowFor(t, series.Annual, "2025-12-31")
	annualLabel := FiscalPeriodLabel(annualRow, yearEndMonth, true)
	if annualLabel == nil || *annualLabel != "FY2025" {
		t.Fatalf("annual fiscal_period = %v, want FY2025", annualLabel)
	}

	newestQuarter := rowFor(t, series.Quarterly, "2026-03-31")
	newestLabel := FiscalPeriodLabel(newestQuarter, yearEndMonth, false)
	if newestLabel == nil || *newestLabel != "2026-Q1" {
		t.Fatalf("fiscal_period = %v, want 2026-Q1 (year-first)", newestLabel)
	}

	priorQuarter := rowFor(t, series.Quarterly, "2025-12-31")
	priorLabel := FiscalPeriodLabel(priorQuarter, yearEndMonth, false)
	if priorLabel == nil || *priorLabel != "2025-Q4" {
		t.Fatalf("fiscal_period = %v, want 2025-Q4", priorLabel)
	}
}

func TestDeriveTTMRows_IncomeStatement(t *testing.T) {
	value := parseFixture(t, statementsFixtureJSON)
	series, err := ParseStatementSeries(value, "income")
	if err != nil {
		t.Fatalf("ParseStatementSeries: %v", err)
	}
	flowLabels := NewLabelSet(
		"Revenue", "Cost of Revenue", "Gross Profit", "Operating Income", "Income Tax",
		"Net Income", "EPS (Basic)", "EPS (Diluted)",
		"Non-Operating Items|Non-Operating Interest Expense",
	)
	meanLabels := NewLabelSet("Shares Outstanding (Basic)")

	ttm := DeriveTTMRows(series.Quarterly, flowLabels, meanLabels)
	if len(ttm) != 1 {
		t.Fatalf("want 1 TTM row from 4 quarters, got %d", len(ttm))
	}
	row := ttm[0]
	if !row.ReportPeriod.Equal(mustDate(t, "2026-03-31")) {
		t.Fatalf("TTM report_period = %s, want 2026-03-31", row.ReportPeriod)
	}
	if got, _ := asFloat(row.Values["Revenue"]); got != 91 { // 20+22+24+25
		t.Errorf("TTM revenue = %v, want 91", got)
	}
	if got, _ := asFloat(row.Values["Net Income"]); got != 23 { // 5+6+6+6
		t.Errorf("TTM net income = %v, want 23", got)
	}
	if got, _ := asFloat(row.Values["Non-Operating Items|Non-Operating Interest Expense"]); got != 6 { // 1+1+2+2
		t.Errorf("TTM interest expense = %v, want 6", got)
	}
	if got, _ := asFloat(row.Values["EPS (Basic)"]); got != 2.1 { // 0.5+0.6+0.5+0.5
		t.Errorf("TTM basic EPS = %v, want 2.1", got)
	}
	if got, _ := asFloat(row.Values["EPS (Diluted)"]); got != 2.0 { // 0.5+0.5+0.5+0.5
		t.Errorf("TTM diluted EPS = %v, want 2.0", got)
	}
	if got, _ := asFloat(row.Values["Shares Outstanding (Basic)"]); got != 10.75 { // mean of 10,10,11,12
		t.Errorf("TTM weighted average shares = %v, want 10.75", got)
	}
}

func TestFilterRows_ReportPeriodGTE(t *testing.T) {
	value := parseFixture(t, statementsFixtureJSON)
	series, err := ParseStatementSeries(value, "income")
	if err != nil {
		t.Fatalf("ParseStatementSeries: %v", err)
	}
	gte := mustDate(t, "2025-01-01")
	filtered := FilterRows(series.Annual, RowFilters{GTE: &gte})
	if len(filtered) != 1 || !filtered[0].ReportPeriod.Equal(mustDate(t, "2025-12-31")) {
		t.Fatalf("filtered = %+v, want just 2025-12-31", filtered)
	}

	exact := mustDate(t, "2024-12-31")
	exactFiltered := FilterRows(series.Annual, RowFilters{Exact: &exact})
	if len(exactFiltered) != 1 {
		t.Fatalf("want 1 exact match, got %d", len(exactFiltered))
	}
	if got, _ := asFloat(exactFiltered[0].Values["Revenue"]); got != 100 {
		t.Errorf("exact match revenue = %v, want 100", got)
	}
}

func TestParseStatementSeries_MissingSection(t *testing.T) {
	_, err := ParseStatementSeries(map[string]any{"balanceSheet": map[string]any{}}, "income")
	if err == nil {
		t.Fatal("want SchemaDriftError for missing income section")
	}
	if _, ok := err.(*SchemaDriftError); !ok {
		t.Fatalf("want *SchemaDriftError, got %T", err)
	}
}
