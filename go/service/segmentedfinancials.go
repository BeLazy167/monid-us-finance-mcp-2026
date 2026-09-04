// This file ports segmented_financials.py: the JSON Schema and instructions
// sent to Context.dev /web/extract for get_segmented_financials, plus
// mapping the extracted product/geographic segment arrays onto Financial
// Datasets records. Financial Datasets has no fixed per-field schema for
// this response (segment labels are filing-specific), so records are built
// as ordered JSON objects (via orderedJSONObject, defined in service.go)
// rather than a fixed Go struct - mirroring segmented_financials.py's own
// direct dict construction (it never calls into fd.py).
package service

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/providers"
)

// segmentArrayItemSchema mirrors segmented_financials.py's
// _SEGMENT_ARRAY_ITEM.
var segmentArrayItemSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"name":   map[string]any{"type": []any{"string", "null"}},
		"metric": map[string]any{"type": []any{"string", "null"}},
		"unit":   map[string]any{"type": []any{"string", "null"}},
		"values": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"fiscal_year": map[string]any{"type": []any{"integer", "null"}},
					"period_end":  map[string]any{"type": []any{"string", "null"}},
					"value":       map[string]any{"type": []any{"number", "null"}},
				},
				"required": []any{"fiscal_year", "period_end", "value"},
			},
		},
		"evidence_quote":   map[string]any{"type": []any{"string", "null"}},
		"evidence_section": map[string]any{"type": []any{"string", "null"}},
	},
	"required": []any{"name", "metric", "unit", "values", "evidence_quote", "evidence_section"},
}

// segmentExtractSchema mirrors segmented_financials.segment_extract_schema.
func segmentExtractSchema() map[string]any {
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"company":           map[string]any{"type": []any{"string", "null"}},
			"filing_type":       map[string]any{"type": []any{"string", "null"}},
			"fiscal_year_end":   map[string]any{"type": []any{"string", "null"}},
			"source_url":        map[string]any{"type": []any{"string", "null"}},
			"product_net_sales": map[string]any{"type": "array", "items": segmentArrayItemSchema},
			"geographic_reportable_segment_net_sales": map[string]any{
				"type": "array", "items": segmentArrayItemSchema,
			},
		},
	}
}

// segmentInstructions mirrors segmented_financials.SEGMENT_INSTRUCTIONS.
const segmentInstructions = "Extract the segment information note from this SEC filing: net sales " +
	"by product or service line, and net sales by geographic reportable " +
	"segment, for every fiscal year shown. Use only numbers stated in the " +
	"filing; leave fields null when the filing does not state them. Report " +
	"each value exactly as printed and put the table's stated scale in unit " +
	"(for example \"millions\" or \"thousands\"), or null if the table " +
	"states none."

// segmentScale converts a figure from the scale the filing's table states
// to whole units. Tables in a 10-K almost always print "in millions" or
// "in thousands"; copying such a figure through unscaled reported Apple's
// FY2025 iPhone revenue as 209586 where the filing means 209,586,000,000
// (measured 2026-09-04 against Financial Datasets' own output). A null or
// unrecognised unit is taken as whole units, which is what a table with no
// stated scale means.
func segmentScale(unit any) float64 {
	text, _ := unit.(string)
	switch {
	case strings.Contains(strings.ToLower(text), "billion"):
		return 1e9
	case strings.Contains(strings.ToLower(text), "million"):
		return 1e6
	case strings.Contains(strings.ToLower(text), "thousand"):
		return 1e3
	default:
		return 1
	}
}

type segmentRow struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type segmentPeriod struct {
	fiscalYear *int
	products   []segmentRow
	segments   []segmentRow
}

// normalizeSegmentedFinancials mirrors
// segmented_financials.normalize_segmented_financials.
// segmentedFinancialRecord pairs one segmented-financials record's ordered
// JSON body with its parsed report_period, so callers can filter by the
// report_period* date filters (mirroring service._segment_matches) without
// re-parsing the "report_period" key back out of the ordered object.
type segmentedFinancialRecord struct {
	ReportPeriod time.Time
	Object       *orderedJSONObject
}

func normalizeSegmentedFinancials(data map[string]any, ticker, filingURL string, accessionNumber *string) ([]segmentedFinancialRecord, error) {
	periods := map[string]*segmentPeriod{}
	var order []string
	collect := func(value any, isProducts bool) error {
		if value == nil {
			return nil
		}
		list, ok := value.([]any)
		if !ok {
			return &providers.SchemaDriftError{Msg: "Segment extraction arrays must be lists or null"}
		}
		for _, rawItem := range list {
			item, ok := rawItem.(map[string]any)
			if !ok {
				return &providers.SchemaDriftError{Msg: "Segment extraction rows must be objects"}
			}
			name, ok := item["name"].(string)
			values, valuesOK := item["values"].([]any)
			if !ok || !valuesOK {
				continue
			}
			for _, rawEntry := range values {
				entry, ok := rawEntry.(map[string]any)
				if !ok {
					continue
				}
				periodEnd, ok := entry["period_end"].(string)
				rawValue, hasValue := entry["value"]
				if !ok || !hasValue || rawValue == nil {
					continue
				}
				numeric, isNumber := rawValue.(float64)
				if !isNumber {
					continue
				}
				day := parseOptDate(periodEnd)
				if day == nil {
					continue
				}
				key := day.Format(dateLayout)
				period, exists := periods[key]
				if !exists {
					period = &segmentPeriod{}
					if fy, ok := entry["fiscal_year"].(float64); ok {
						v := int(fy)
						period.fiscalYear = &v
					}
					periods[key] = period
					order = append(order, key)
				}
				row := segmentRow{Label: name, Value: numeric * segmentScale(item["unit"])}
				if isProducts {
					period.products = append(period.products, row)
				} else {
					period.segments = append(period.segments, row)
				}
			}
		}
		return nil
	}
	if err := collect(data["product_net_sales"], true); err != nil {
		return nil, err
	}
	if err := collect(data["geographic_reportable_segment_net_sales"], false); err != nil {
		return nil, err
	}
	if len(periods) == 0 {
		return nil, &providers.SchemaDriftError{Msg: "Segment extraction returned no periods with values"}
	}

	sort.Sort(sort.Reverse(sort.StringSlice(order)))
	records := make([]segmentedFinancialRecord, 0, len(order))
	for _, key := range order {
		period := periods[key]
		reportDay, _ := time.Parse(dateLayout, key)
		record := newOrderedJSONObject()
		record.set("ticker", ticker)
		record.set("report_period", key)
		if period.fiscalYear != nil {
			record.set("fiscal_period", "FY"+strconv.Itoa(*period.fiscalYear))
		}
		record.set("period", "annual")
		if accessionNumber != nil {
			record.set("accession_number", *accessionNumber)
		}
		record.set("filing_url", filingURL)
		income := map[string]any{}
		revenue := map[string]any{}
		if len(period.products) > 0 {
			revenue["product"] = period.products
		}
		if len(period.segments) > 0 {
			revenue["segment"] = period.segments
		}
		if len(revenue) > 0 {
			income["revenue"] = revenue
			record.set("income_statement", income)
		}
		records = append(records, segmentedFinancialRecord{ReportPeriod: reportDay, Object: record})
	}
	return records, nil
}
