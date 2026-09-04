// Package providers ports the DefiLlama/Context.dev/Nasdaq/SECForm4 provider
// payload shapes into Financial Datasets (FD) records.
//
// This file also holds the small generic JSON-shape helpers shared by the
// filings, prices, news, and summary providers in this scope: extracting a
// record list from one of a few well-known wrapper keys, picking the first
// present field among provider-specific aliases, and sorting provider
// records by the newest plausible date field. These mirror the shared
// helpers in src/monid_finance_mcp/providers/us/normalize.py.
//
// Everything in this file is scoped to filings.go/prices.go/news.go/
// summary.go/screener.go/insider.go, except for the shared error types
// (InputError/SchemaDriftError/UnsupportedError, go/providers/errors.go)
// and DeriveAccession (go/providers/sec.go), which are the package-wide
// canonical helpers every provider file uses.
package providers

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/belazy/monid-finance/fd"
)

// numberValue reports whether v is a JSON number: encoding/json decodes
// every JSON number into float64 (and every JSON boolean into bool) when
// unmarshaled into `any`, so this alone matches Python's
// isinstance(v, int | float) and not isinstance(v, bool).
func numberValue(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// dateKeys is the fallback order normalize.py uses to guess a record's date
// for sorting, newest sourced key first.
var dateKeys = []string{
	"report_period",
	"reportDate",
	"report_date",
	"periodEnding",
	"filingDate",
	"filing_date",
	"published_at",
	"publishedAt",
	"date",
	"end_date",
}

// unmarshalAny parses raw JSON into a generic Go value (map[string]any,
// []any, string, float64, bool, or nil).
func unmarshalAny(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, schemaDriftf("provider payload is not valid JSON")
	}
	return value, nil
}

// toObjectRecords requires every element of values to be a JSON object.
func toObjectRecords(values []any, name string) ([]map[string]any, error) {
	records := make([]map[string]any, 0, len(values))
	for index, item := range values {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, schemaDriftf("%s[%d] is not an object", name, index)
		}
		records = append(records, obj)
	}
	return records, nil
}

// extractRecords finds the record list nested under one of keys (or the
// payload itself, if it is already a list), mirroring
// normalize._extract_records.
func extractRecords(raw json.RawMessage, keys []string) ([]map[string]any, error) {
	current, err := unmarshalAny(raw)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 5; i++ {
		if arr, ok := current.([]any); ok {
			return toObjectRecords(arr, "records")
		}
		obj, ok := current.(map[string]any)
		if !ok {
			break
		}
		found := false
		for _, key := range keys {
			child, exists := obj[key]
			if !exists {
				continue
			}
			if arr, ok := child.([]any); ok {
				return toObjectRecords(arr, key)
			}
			if childObj, ok := child.(map[string]any); ok {
				current = childObj
				found = true
				break
			}
		}
		if !found {
			break
		}
	}
	return nil, schemaDriftf("provider payload omitted the expected record list")
}

// firstValue returns the first key present in record among keys, or nil.
func firstValue(record map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := record[key]; ok {
			return value
		}
	}
	return nil
}

// firstStringPtr is firstValue narrowed to a non-empty string, or nil.
func firstStringPtr(record map[string]any, keys ...string) *string {
	value := firstValue(record, keys...)
	if s, ok := value.(string); ok && s != "" {
		return &s
	}
	return nil
}

// firstStringVal is firstStringPtr with "" instead of nil.
func firstStringVal(record map[string]any, keys ...string) string {
	if p := firstStringPtr(record, keys...); p != nil {
		return *p
	}
	return ""
}

// recordDate reads the first ten characters of the first date-shaped field
// present among keys and reports whether they parse as YYYY-MM-DD.
func recordDate(record map[string]any, keys []string) (string, bool) {
	for _, key := range keys {
		value, ok := record[key]
		if !ok {
			continue
		}
		s, ok := value.(string)
		if !ok || s == "" {
			continue
		}
		if len(s) < 10 {
			continue
		}
		day := s[:10]
		if !isISODate(day) {
			continue
		}
		return day, true
	}
	return "", false
}

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func isISODate(s string) bool { return isoDateRe.MatchString(s) }

// sortRecordsByDateDesc sorts records newest-first using recordDate(dateKeys),
// preserving relative order for records with the same (or no) date -
// mirroring normalize._record_sort_key under Python's stable reverse sort.
func sortRecordsByDateDesc(records []map[string]any) {
	sort.SliceStable(records, func(i, j int) bool {
		di, oki := recordDate(records[i], dateKeys)
		dj, okj := recordDate(records[j], dateKeys)
		if oki != okj {
			return oki // dated records sort before undated ones
		}
		if !oki {
			return false
		}
		return di > dj
	})
}

// filterRecordsByDateRange keeps only records whose recordDate(keys) falls
// within [minimum, maximum] (either bound optional), dropping undated
// records once a bound is given - mirroring normalize._filter_records_by_date.
func filterRecordsByDateRange(records []map[string]any, keys []string, minimum, maximum *string) []map[string]any {
	if minimum == nil && maximum == nil {
		return records
	}
	filtered := make([]map[string]any, 0, len(records))
	for _, record := range records {
		day, ok := recordDate(record, keys)
		if !ok {
			continue
		}
		if minimum != nil && day < *minimum {
			continue
		}
		if maximum != nil && day > *maximum {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

// filingDateKeys is the alias order used specifically for filing_date
// bounds, mirroring the keys= override normalize_filings passes to
// _filter_records_by_date.
var filingDateKeys = []string{"filingDate", "filing_date", "date"}

// filingTypeEnum is the Financial Datasets Filing.filing_type enum.
var filingTypeEnum = map[string]bool{
	"10-K": true, "10-Q": true, "8-K": true, "20-F": true, "6-K": true,
}

// ValidateFilingTypes normalizes and validates filing_type values against
// the Financial Datasets Filing.filing_type enum, mirroring
// normalize.validate_filing_types. A nil slice means "no filter" (returns
// nil, nil); an empty or invalid slice is a bad_request error.
func ValidateFilingTypes(values []string) (map[string]bool, error) {
	if values == nil {
		return nil, nil
	}
	normalized := make(map[string]bool, len(values))
	for _, value := range values {
		normalized[strings.ToUpper(strings.TrimSpace(value))] = true
	}
	if len(normalized) == 0 {
		return nil, badFilingTypeError()
	}
	for key := range normalized {
		if !filingTypeEnum[key] {
			return nil, badFilingTypeError()
		}
	}
	return normalized, nil
}

func badFilingTypeError() error {
	allowed := make([]string, 0, len(filingTypeEnum))
	for key := range filingTypeEnum {
		allowed = append(allowed, key)
	}
	sort.Strings(allowed)
	return newInputErrorf("filing_type values must be one of: %s", strings.Join(allowed, ", "))
}

// NormalizeFilings parses a DefiLlama /equities/v1/filings payload into FD
// Filing records, mirroring normalize.normalize_filings + fd.filing_record.
//
//   - filingTypes, if non-nil, keeps only records whose form is a member.
//   - filingDateGTE / filingDateLTE (YYYY-MM-DD, optional) bound the
//     filing_date/filingDate/date field.
//   - Records are sorted newest-first (see sortRecordsByDateDesc) and
//     truncated to limit.
func NormalizeFilings(raw json.RawMessage, ticker string, filingTypes map[string]bool, limit int, filingDateGTE, filingDateLTE *string) ([]fd.Filing, error) {
	records, err := extractRecords(raw, []string{"filings", "results", "data"})
	if err != nil {
		return nil, err
	}
	if filingTypes != nil {
		filtered := make([]map[string]any, 0, len(records))
		for _, record := range records {
			form := strings.ToUpper(firstStringVal(record, "form", "filing_type", "type"))
			if form != "" && filingTypes[form] {
				filtered = append(filtered, record)
			}
		}
		records = filtered
	}
	records = filterRecordsByDateRange(records, filingDateKeys, filingDateGTE, filingDateLTE)
	sortRecordsByDateDesc(records)
	if limit >= 0 && len(records) > limit {
		records = records[:limit]
	}

	filings := make([]fd.Filing, 0, len(records))
	for _, record := range records {
		urlValue := firstStringPtr(record, "primary_document_url", "primaryDocumentUrl", "url")
		var accession *string
		if urlValue != nil {
			accession = DeriveAccession(*urlValue)
		}
		symbol := ticker
		filings = append(filings, fd.Filing{
			AccessionNumber: accession,
			FilingType:      firstStringPtr(record, "form", "filing_type", "type"),
			ReportDate:      firstStringPtr(record, "report_date", "reportDate"),
			FilingDate:      firstStringPtr(record, "filing_date", "filingDate", "date"),
			Ticker:          &symbol,
			URL:             urlValue,
		})
	}
	return filings, nil
}
