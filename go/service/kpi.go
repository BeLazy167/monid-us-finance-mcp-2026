// This file ports kpi.py: JSON Schema extraction bodies and instructions
// for get_kpi_metrics/get_kpi_guidance/get_kpi_non_gaap, plus the shared
// parse_kpi_items normalization each of those three tools composes with a
// different fd record shape.
package service

import (
	"strings"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

// kpiItemSchema mirrors kpi.py's _KPI_ITEM: one extracted KPI's shape.
var kpiItemSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"name":           map[string]any{"type": []any{"string", "null"}},
		"unit":           map[string]any{"type": []any{"string", "null"}},
		"period":         map[string]any{"type": []any{"string", "null"}},
		"value_text":     map[string]any{"type": []any{"string", "null"}},
		"value":          map[string]any{"type": []any{"number", "null"}},
		"basis":          map[string]any{"type": []any{"string", "null"}},
		"evidence_quote": map[string]any{"type": []any{"string", "null"}},
	},
	"required": []any{"name", "unit", "period", "value_text", "value", "basis", "evidence_quote"},
}

// kpiSchema mirrors kpi.py's _kpi_schema(description).
func kpiSchema(description string) map[string]any {
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"kpis": map[string]any{"type": "array", "items": kpiItemSchema},
		},
		"title": description,
	}
}

// kpiMetricsExtractSchema mirrors kpi.kpi_metrics_extract_schema.
func kpiMetricsExtractSchema() map[string]any {
	return kpiSchema("Operational key performance indicators")
}

// kpiGuidanceExtractSchema mirrors kpi.kpi_guidance_extract_schema.
func kpiGuidanceExtractSchema() map[string]any { return kpiSchema("Forward guidance") }

// kpiNonGAAPExtractSchema mirrors kpi.kpi_nongaap_extract_schema.
func kpiNonGAAPExtractSchema() map[string]any { return kpiSchema("Non-GAAP financial metrics") }

// kpisBaseInstructions mirrors kpi.KPIS_BASE_INSTRUCTIONS, a %s-style
// template filled by kind.
const kpisBaseInstructions = "Extract from this earnings filing each disclosed %s. For every item " +
	"return: name - a canonical snake_case metric name (e.g. load_factor, " +
	"cet1_ratio, same_store_sales); unit - the unit of measure the filing uses " +
	"(% , cents, USD, USD per share, count, etc.); period - the fiscal period " +
	"label exactly as the filing states it (e.g. Q4 2025 or FY 2025); " +
	"value_text - the number exactly as printed in the filing; value - the same " +
	"number as a plain number; basis - exactly \"quarterly\" or \"annual\" " +
	"according to the fiscal period being reported; evidence_quote - the " +
	"sentence(s) from the filing that state the item. Use only numbers stated " +
	"in the filing; leave fields null when the filing does not state them. " +
	"Return an empty kpis list when none are disclosed."

// kpiMetricsInstructions mirrors kpi.KPI_METRICS_INSTRUCTIONS.
var kpiMetricsInstructions = kpiInstructions("operational key performance indicators that do not appear on the " +
	"standard financial statements, such as load factor, same-store sales, " +
	"FFO per share, CET1 ratio, DAUs, or ARPU")

// kpiGuidanceInstructions mirrors kpi.KPI_GUIDANCE_INSTRUCTIONS.
var kpiGuidanceInstructions = kpiInstructions("forward guidance, such as a guided revenue range, margin range, or " +
	"EPS outlook; period is the forward fiscal period that is being guided")

// kpiNonGAAPInstructions mirrors kpi.KPI_NONGAAP_INSTRUCTIONS.
var kpiNonGAAPInstructions = kpiInstructions("non-GAAP adjusted financial metrics, such as adjusted EPS, adjusted " +
	"EBITDA, or free cash flow")

func kpiInstructions(kind string) string {
	return strings.Replace(kpisBaseInstructions, "%s", kind, 1)
}

// kpiItem mirrors kpi.KpiItem.
type kpiItem struct {
	Name          string
	Unit          *string
	Period        *string
	ValueText     *string
	Value         *float64
	Basis         *string
	EvidenceQuote *string
}

// parseKPIItems mirrors kpi.parse_kpi_items: parses the extracted "kpis"
// list from a KPI extract envelope's data object.
func parseKPIItems(data map[string]any) ([]kpiItem, error) {
	raw, exists := data["kpis"]
	if !exists || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "KPI extraction kpis must be an array or null"}
	}
	items := make([]kpiItem, 0, len(list))
	for _, rawItem := range list {
		obj, ok := rawItem.(map[string]any)
		if !ok {
			return nil, &providers.SchemaDriftError{Msg: "KPI extraction kpis item must be an object"}
		}
		name, ok := obj["name"].(string)
		if !ok || name == "" {
			continue
		}
		items = append(items, kpiItem{
			Name:          name,
			Unit:          kpiOptString(obj["unit"]),
			Period:        kpiOptString(obj["period"]),
			ValueText:     kpiOptString(obj["value_text"]),
			Value:         kpiFiniteNumber(obj["value"]),
			Basis:         kpiOptString(obj["basis"]),
			EvidenceQuote: kpiOptString(obj["evidence_quote"]),
		})
	}
	return items, nil
}

func kpiOptString(value any) *string {
	if s, ok := value.(string); ok && s != "" {
		return &s
	}
	return nil
}

func kpiFiniteNumber(value any) *float64 {
	f, ok := value.(float64)
	if !ok {
		return nil
	}
	return &f
}

func kpiMetricKey(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), "_")
}

// kpiMatches mirrors kpi._matches.
func kpiMatches(item kpiItem, period, metricName *string) bool {
	if period != nil {
		basis := ""
		if item.Basis != nil {
			basis = strings.ToLower(*item.Basis)
		}
		if basis != *period {
			return false
		}
	}
	if metricName != nil && kpiMetricKey(item.Name) != kpiMetricKey(*metricName) {
		return false
	}
	return true
}

// kpiComplete mirrors kpi._complete: required Financial Datasets fields
// must all be present to emit a record.
func kpiComplete(item kpiItem) bool {
	return item.Unit != nil && *item.Unit != "" &&
		item.Period != nil && *item.Period != "" &&
		item.Basis != nil && *item.Basis != ""
}

func kpiStr(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// normalizeKPIMetrics mirrors kpi.normalize_kpi_metrics + fd.kpi_metric_record.
func normalizeKPIMetrics(data map[string]any, ticker, filingURL string, period, metricName *string) ([]fd.KPIMetric, error) {
	items, err := parseKPIItems(data)
	if err != nil {
		return nil, err
	}
	var records []fd.KPIMetric
	for _, item := range items {
		if !kpiMatches(item, period, metricName) || !kpiComplete(item) {
			continue
		}
		tickerCopy, metricNameCopy := ticker, item.Name
		unit, periodLabel, periodType, sourceURL := kpiStr(item.Unit), kpiStr(item.Period), kpiStr(item.Basis), filingURL
		record := fd.KPIMetric{
			Ticker:     &tickerCopy,
			MetricName: &metricNameCopy,
			Value:      item.Value,
			Unit:       &unit,
			Period:     &periodLabel,
			PeriodType: &periodType,
			SourceText: item.EvidenceQuote,
			SourceURL:  &sourceURL,
		}
		records = append(records, record)
	}
	return records, nil
}

// normalizeKPIGuidance mirrors kpi.normalize_kpi_guidance + fd.kpi_guidance_item_record.
func normalizeKPIGuidance(data map[string]any, ticker, filingURL string, period, metricName *string) ([]fd.KPIGuidanceItem, error) {
	items, err := parseKPIItems(data)
	if err != nil {
		return nil, err
	}
	var records []fd.KPIGuidanceItem
	for _, item := range items {
		if !kpiMatches(item, period, metricName) || !kpiComplete(item) {
			continue
		}
		tickerCopy, metricNameCopy := ticker, item.Name
		unit, periodLabel, periodType, sourceURL := kpiStr(item.Unit), kpiStr(item.Period), kpiStr(item.Basis), filingURL
		record := fd.KPIGuidanceItem{
			Ticker:     &tickerCopy,
			MetricName: &metricNameCopy,
			Value:      item.Value,
			Unit:       &unit,
			Period:     &periodLabel,
			PeriodType: &periodType,
			RawText:    item.ValueText,
			SourceText: item.EvidenceQuote,
			SourceURL:  &sourceURL,
		}
		records = append(records, record)
	}
	return records, nil
}

// normalizeKPINonGAAP mirrors kpi.normalize_kpi_nongaap + fd.kpi_non_gaap_metric_record.
func normalizeKPINonGAAP(data map[string]any, ticker, filingURL string, period, metricName *string) ([]fd.KPINonGAAPMetric, error) {
	items, err := parseKPIItems(data)
	if err != nil {
		return nil, err
	}
	var records []fd.KPINonGAAPMetric
	for _, item := range items {
		if !kpiMatches(item, period, metricName) || !kpiComplete(item) {
			continue
		}
		tickerCopy, metricNameCopy := ticker, item.Name
		unit, periodLabel, periodType, sourceURL := kpiStr(item.Unit), kpiStr(item.Period), kpiStr(item.Basis), filingURL
		record := fd.KPINonGAAPMetric{
			Ticker:     &tickerCopy,
			MetricName: &metricNameCopy,
			Value:      item.Value,
			Unit:       &unit,
			Period:     &periodLabel,
			PeriodType: &periodType,
			SourceText: item.EvidenceQuote,
			SourceURL:  &sourceURL,
		}
		records = append(records, record)
	}
	return records, nil
}
