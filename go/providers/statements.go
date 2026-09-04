// Package providers holds the DefiLlama-to-Financial-Datasets parsing that
// used to live in monid_finance_mcp/providers/us/*.py. Every function here
// is a direct port of the matching Python module; the Python source is the
// executable spec and every number, label, and key order was verified
// against the live Financial Datasets API.
package providers

import (
	"fmt"
	"sort"
	"time"
)

// dateLayout is the ISO 8601 date-only layout used for report periods
// throughout this package (Python's date.fromisoformat/.isoformat()).
const dateLayout = "2006-01-02"

// statementSections lists the DefiLlama section-key aliases per statement.
// Ported from statements.STATEMENT_SECTIONS.
var statementSections = map[string][]string{
	"income":  {"incomeStatement", "income_statement", "income"},
	"balance": {"balanceSheet", "balance_sheet", "balance"},
	"cash":    {"cashflow", "cashFlow", "cash_flow", "cashFlowStatement"},
}

// PeriodRow is one report period of one statement, with plain-label values.
// Ported from statements.PeriodRow.
type PeriodRow struct {
	ReportPeriod time.Time
	Values       map[string]any
}

// StatementSeries holds the annual and quarterly rows of one statement.
// Ported from statements.StatementSeries.
type StatementSeries struct {
	Annual    []PeriodRow
	Quarterly []PeriodRow
}

// ParseStatementSeries parses the annual and quarterly matrices of one
// statement ("income", "balance", or "cash") from a DefiLlama
// /equities/v1/statements payload. Ported from
// statements.parse_statement_series.
func ParseStatementSeries(value any, statement string) (StatementSeries, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return StatementSeries{}, schemaDriftf("DefiLlama statements payload must be an object")
	}
	section, err := sectionRoot(root, statement)
	if err != nil {
		return StatementSeries{}, err
	}
	annual, err := parseMatrix(section, statement, "annual")
	if err != nil {
		return StatementSeries{}, err
	}
	quarterly, err := parseMatrix(section, statement, "quarterly")
	if err != nil {
		return StatementSeries{}, err
	}
	return StatementSeries{Annual: annual, Quarterly: quarterly}, nil
}

// FiscalYearEndMonth learns the fiscal year end month from the annual
// series. Ported from statements.fiscal_year_end_month.
func FiscalYearEndMonth(series StatementSeries) *int {
	return fiscalYearEndMonthOfRows(series.Annual)
}

func fiscalYearEndMonthOfRows(rows []PeriodRow) *int {
	months := map[int]struct{}{}
	for _, row := range rows {
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

// FiscalPeriodLabel derives the Financial Datasets fiscal_period label for
// one row: "FY2025" for annual rows, "2026-Q3" (year-first) for quarterly
// rows. Ported from statements.fiscal_period_label.
func FiscalPeriodLabel(row PeriodRow, yearEndMonth *int, isAnnual bool) *string {
	return fiscalPeriodLabel(row.ReportPeriod, yearEndMonth, isAnnual)
}

// fiscalPeriodLabel is the shared implementation used by statements.go,
// metrics.go, and earnings.go alike (all three Python modules compute this
// identically).
func fiscalPeriodLabel(reportPeriod time.Time, yearEndMonth *int, isAnnual bool) *string {
	if isAnnual {
		label := fmt.Sprintf("FY%d", reportPeriod.Year())
		return &label
	}
	if yearEndMonth == nil {
		return nil
	}
	year := reportPeriod.Year()
	if int(reportPeriod.Month()) > *yearEndMonth {
		year++
	}
	quarter := (int(reportPeriod.Month())-1)/3 + 1
	label := fmt.Sprintf("%d-Q%d", year, quarter)
	return &label
}

// quarterOrdinal is a monotonic quarter index, shared across the package.
func quarterOrdinal(t time.Time) int {
	return t.Year()*4 + (int(t.Month())-1)/3
}

// LabelSet is a membership set of statement labels, used to classify TTM
// aggregation: flows are summed, means are averaged, everything else is a
// point-in-time balance carried from the final quarter.
type LabelSet map[string]struct{}

// NewLabelSet builds a LabelSet from the given labels.
func NewLabelSet(labels ...string) LabelSet {
	set := make(LabelSet, len(labels))
	for _, label := range labels {
		set[label] = struct{}{}
	}
	return set
}

func (s LabelSet) has(label string) bool {
	_, ok := s[label]
	return ok
}

// DeriveTTMRows derives TTM rows from windows of four consecutive quarters.
// Flow labels are summed across the window; mean labels (weighted-average
// share counts) are averaged; every other label keeps the value from the
// final quarter of the window (point-in-time balances). Ported from
// statements.derive_ttm_rows.
func DeriveTTMRows(quarterly []PeriodRow, flowLabels, meanLabels LabelSet) []PeriodRow {
	ordered := make([]PeriodRow, len(quarterly))
	copy(ordered, quarterly)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ReportPeriod.Before(ordered[j].ReportPeriod)
	})

	var rows []PeriodRow
	for start := 0; start+4 <= len(ordered); start++ {
		window := ordered[start : start+4]
		if !consecutiveQuarters(window) {
			continue
		}
		last := window[3]
		labels := map[string]struct{}{}
		for _, row := range window {
			for label := range row.Values {
				labels[label] = struct{}{}
			}
		}
		values := map[string]any{}
		for label := range labels {
			numbers := make([]float64, 0, 4)
			for _, row := range window {
				if v, ok := row.Values[label]; ok {
					if f, isNum := asFloat(v); isNum {
						numbers = append(numbers, f)
					}
				}
			}
			if len(numbers) != 4 {
				continue
			}
			switch {
			case flowLabels.has(label):
				sum := 0.0
				for _, n := range numbers {
					sum += n
				}
				values[label] = sum
			case meanLabels.has(label):
				sum := 0.0
				for _, n := range numbers {
					sum += n
				}
				values[label] = sum / 4
			default:
				values[label] = numbers[3]
			}
		}
		rows = append(rows, PeriodRow{ReportPeriod: last.ReportPeriod, Values: values})
	}
	return rows
}

// RowFilters are the Financial Datasets report_period comparison filters.
type RowFilters struct {
	Exact *time.Time
	GTE   *time.Time
	LTE   *time.Time
	GT    *time.Time
	LT    *time.Time
}

// FilterRows applies the Financial Datasets report_period comparison
// filters. Ported from statements.filter_rows.
func FilterRows(rows []PeriodRow, filters RowFilters) []PeriodRow {
	var kept []PeriodRow
	for _, row := range rows {
		if rowMatches(row.ReportPeriod, filters) {
			kept = append(kept, row)
		}
	}
	return kept
}

func rowMatches(day time.Time, filters RowFilters) bool {
	if filters.Exact != nil && !day.Equal(*filters.Exact) {
		return false
	}
	if filters.GTE != nil && day.Before(*filters.GTE) {
		return false
	}
	if filters.LTE != nil && day.After(*filters.LTE) {
		return false
	}
	if filters.GT != nil && !day.After(*filters.GT) {
		return false
	}
	if filters.LT != nil && !day.Before(*filters.LT) {
		return false
	}
	return true
}

func consecutiveQuarters(window []PeriodRow) bool {
	previous := -1
	for _, row := range window {
		ordinal := quarterOrdinal(row.ReportPeriod)
		if previous != -1 && ordinal != previous+1 {
			return false
		}
		previous = ordinal
	}
	return true
}

func sectionRoot(root map[string]any, statement string) (map[string]any, error) {
	keys, ok := statementSections[statement]
	if !ok {
		return nil, schemaDriftf("unknown statement %q", statement)
	}
	for _, key := range keys {
		if section, ok := root[key].(map[string]any); ok {
			return section, nil
		}
	}
	return nil, schemaDriftf("DefiLlama payload omitted the %s statement", statement)
}

func parseMatrix(section map[string]any, statement, period string) ([]PeriodRow, error) {
	blockValue, present := section[period]
	if !present || blockValue == nil {
		return nil, nil
	}
	block, ok := blockValue.(map[string]any)
	if !ok {
		return nil, schemaDriftf("DefiLlama %s.%s has an unknown shape", statement, period)
	}
	labels, err := stringList(section["labels"], fmt.Sprintf("DefiLlama %s labels", statement))
	if err != nil {
		return nil, err
	}
	dates, err := dateListTruncated(block["periodEnding"], fmt.Sprintf("DefiLlama %s.%s.periodEnding", statement, period))
	if err != nil {
		return nil, err
	}
	values, err := valueRowsLoose(block["values"], len(labels), len(dates), fmt.Sprintf("DefiLlama %s.%s.values", statement, period))
	if err != nil {
		return nil, err
	}
	rows := make([]PeriodRow, len(dates))
	for i, day := range dates {
		rows[i] = PeriodRow{ReportPeriod: day, Values: map[string]any{}}
	}
	for rowIndex, label := range labels {
		for column := range dates {
			if item := values[rowIndex][column]; item != nil {
				rows[column].Values[label] = item
			}
		}
	}
	childrenBlock, _ := block["children"].(map[string]any)
	childDefinitions, _ := section["children"].(map[string]any)
	if childrenBlock != nil && childDefinitions != nil {
		if err := parseChildren(childrenBlock, childDefinitions, period, dates, rows, fmt.Sprintf("DefiLlama %s.%s", statement, period)); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func parseChildren(childrenBlock, childDefinitions map[string]any, period string, dates []time.Time, rows []PeriodRow, name string) error {
	definitionsValue, ok := childDefinitions[period]
	if !ok {
		return nil
	}
	definitions, ok := definitionsValue.(map[string]any)
	if !ok {
		return nil
	}
	for parent, parentBlockValue := range childrenBlock {
		parentBlock, ok := parentBlockValue.(map[string]any)
		if !ok {
			return schemaDriftf("%s children must map parents to blocks", name)
		}
		definitionValue, ok := definitions[parent]
		if !ok || definitionValue == nil {
			continue
		}
		definition, ok := definitionValue.(map[string]any)
		if !ok {
			return schemaDriftf("%s child labels for %s must be a list", name, parent)
		}
		childLabelsValue, hasLabels := definition["labels"]
		childValuesValue, hasValues := parentBlock["values"]
		if (!hasLabels || childLabelsValue == nil) && (!hasValues || childValuesValue == nil) {
			continue
		}
		rawLabels, ok := childLabelsValue.([]any)
		if !ok {
			return schemaDriftf("%s child labels for %s must be a list", name, parent)
		}
		if _, ok := childValuesValue.([]any); !ok {
			return schemaDriftf("%s child values for %s must be a list", name, parent)
		}
		childLabels := make([]string, 0, len(rawLabels))
		for _, item := range rawLabels {
			if s, ok := item.(string); ok {
				childLabels = append(childLabels, s)
			}
		}
		childValues, err := valueRowsLoose(childValuesValue, len(childLabels), len(dates), fmt.Sprintf("%s children of %s", name, parent))
		if err != nil {
			return err
		}
		for rowIndex, label := range childLabels {
			for column := range dates {
				if item := childValues[rowIndex][column]; item != nil {
					rows[column].Values[parent+"|"+label] = item
				}
			}
		}
	}
	return nil
}

func stringList(value any, name string) ([]string, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, schemaDriftf("%s must be a list", name)
	}
	labels := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, schemaDriftf("%s must contain strings", name)
		}
		labels = append(labels, s)
	}
	return labels, nil
}

// dateListTruncated parses ISO date-time strings, keeping only the first 10
// characters (statements.py's date.fromisoformat(item[:10])).
func dateListTruncated(value any, name string) ([]time.Time, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, schemaDriftf("%s must be a list", name)
	}
	dates := make([]time.Time, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, schemaDriftf("%s must contain strings", name)
		}
		if len(s) > 10 {
			s = s[:10]
		}
		day, err := time.Parse(dateLayout, s)
		if err != nil {
			return nil, schemaDriftf("%s contains a non-ISO date", name)
		}
		dates = append(dates, day)
	}
	return dates, nil
}

// valueRowsLoose returns the raw matrix rows without numeric validation
// (statements.py's own _value_rows only checks list shape).
func valueRowsLoose(value any, rowCount, columnCount int, name string) ([][]any, error) {
	items, ok := value.([]any)
	if !ok || len(items) != rowCount {
		return nil, schemaDriftf("%s row count does not match labels", name)
	}
	rows := make([][]any, len(items))
	for rowIndex, rawRow := range items {
		row, ok := rawRow.([]any)
		if !ok || len(row) != columnCount {
			return nil, schemaDriftf("%s row %d has the wrong width", name, rowIndex)
		}
		rows[rowIndex] = row
	}
	return rows, nil
}

// asFloat reports whether v is a JSON number (encoding/json decodes all
// JSON numbers into float64 when unmarshaled into `any`, and booleans
// decode to bool, so this alone matches Python's
// isinstance(v, int | float) and not isinstance(v, bool)).
func asFloat(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}
