// This file ports the shared validators, date-filter plumbing, and the
// DefiLlama company-catalog lookup that normalize.py holds in Python:
// validate_ticker, validate_period, validate_interval, validate_limit,
// validate_date, validate_date_range, find_company, plus service.py's
// _date_filters/_apply_filing_filters helpers and the FilingIdentity join
// type (fd.py's FilingIdentity dataclass). These live in package service
// (not providers) because providers/us/normalize.py has not been ported to
// go/providers yet - see go/providers/filingitems.go's tickerRE comment,
// which duplicates validate_ticker for the same reason. Every tool call
// path goes through these before any Monid run, so a bad request costs the
// caller nothing.
package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/belazy/monid-finance/providers"
)

const dateLayout = "2006-01-02"

var tickerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,19}$`)

var validPeriods = map[string]bool{"annual": true, "quarterly": true, "ttm": true}
var validKPIPeriods = map[string]bool{"annual": true, "quarterly": true}
var validIntervals = map[string]bool{"day": true, "week": true, "month": true, "year": true}

// validFilingTypeEnum is the set every filing_type argument is checked
// against, built once from edgarFormTypes (filingtypes_gen.go), which is
// generated from SEC EDGAR's own quarterly form indexes.
//
// This used to be a hand-typed five-entry set (10-K, 10-Q, 8-K, 20-F,
// 6-K), which rejected every other real form type: a caller filtering on
// S-1 or DEF 14A got a bad_request for a filing SEC genuinely publishes.
// Financial Datasets accepts 500 form types (measured live 2026-09-04);
// this set covers all of them and 215 more.
var validFilingTypeEnum = func() map[string]bool {
	set := make(map[string]bool, len(edgarFormTypes))
	for _, t := range edgarFormTypes {
		set[t] = true
	}
	return set
}()

// validateTicker mirrors normalize.validate_ticker.
func validateTicker(value string) (string, error) {
	ticker := strings.ToUpper(strings.TrimSpace(value))
	if !tickerPattern.MatchString(ticker) {
		return "", &providers.InputError{Msg: "ticker must be 1-20 letters, digits, dots, or hyphens"}
	}
	return ticker, nil
}

// validatePeriod mirrors normalize.validate_period.
func validatePeriod(value string) (string, error) {
	period := strings.ToLower(strings.TrimSpace(value))
	if !validPeriods[period] {
		return "", &providers.InputError{Msg: "period must be annual, quarterly, or ttm"}
	}
	return period, nil
}

// validateKPIPeriod mirrors kpi.validate_kpi_period.
func validateKPIPeriod(value string) (string, error) {
	period := strings.ToLower(strings.TrimSpace(value))
	if !validKPIPeriods[period] {
		return "", &providers.InputError{Msg: "period must be quarterly or annual"}
	}
	return period, nil
}

// validateInterval mirrors normalize.validate_interval.
func validateInterval(value string) (string, error) {
	interval := strings.ToLower(strings.TrimSpace(value))
	if !validIntervals[interval] {
		return "", &providers.InputError{Msg: "interval must be day, week, month, or year"}
	}
	return interval, nil
}

// validateLimit mirrors normalize.validate_limit.
func validateLimit(value, maximum int) (int, error) {
	if value < 1 || value > maximum {
		return 0, &providers.InputError{Msg: fmt.Sprintf("limit must be between 1 and %d", maximum)}
	}
	return value, nil
}

// validateDate mirrors normalize.validate_date. A nil value returns (nil, nil).
func validateDate(value *string, name string) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, *value)
	if err != nil || t.Format(dateLayout) != *value {
		return nil, &providers.InputError{Msg: name + " must use YYYY-MM-DD"}
	}
	return &t, nil
}

// validateDateRange mirrors normalize.validate_date_range.
func validateDateRange(start, end *string, startName, endName string) (*time.Time, *time.Time, error) {
	startDay, err := validateDate(start, startName)
	if err != nil {
		return nil, nil, err
	}
	endDay, err := validateDate(end, endName)
	if err != nil {
		return nil, nil, err
	}
	if startDay != nil && endDay != nil && startDay.After(*endDay) {
		return nil, nil, &providers.InputError{Msg: fmt.Sprintf("%s must not be after %s", startName, endName)}
	}
	return startDay, endDay, nil
}

// validateFilingTypes mirrors normalize.validate_filing_types: nil means "no
// filter"; an empty or invalid set is a bad_request error.
func validateFilingTypes(values []string) (map[string]bool, error) {
	if values == nil {
		return nil, nil
	}
	normalized := make(map[string]bool, len(values))
	for _, v := range values {
		normalized[strings.ToUpper(strings.TrimSpace(v))] = true
	}
	if len(normalized) == 0 {
		return nil, badFilingTypeError()
	}
	for key := range normalized {
		if !validFilingTypeEnum[key] {
			return nil, badFilingTypeError()
		}
	}
	return normalized, nil
}

// badFilingTypeError names the catalog route rather than inlining it.
// The enum holds hundreds of SEC form types, so listing them in an error
// message would bury the actual problem in kilobytes of noise.
func badFilingTypeError() error {
	return &providers.InputError{Msg: fmt.Sprintf(
		"filing_type must be a recognized SEC EDGAR form type; this server accepts %d of them. "+
			"Fetch the full list from /filings/types (it costs nothing and makes no upstream call).",
		len(validFilingTypeEnum))}
}

// dateFilters mirrors service._date_filters's returned dict: the five
// optional report_period/filing_date comparison bounds shared by every
// statement/metrics/insider-trades/institutional-holdings tool.
type dateFilters struct {
	Exact *time.Time
	GTE   *time.Time
	LTE   *time.Time
	GT    *time.Time
	LT    *time.Time
}

// any reports whether at least one bound was supplied.
func (f dateFilters) any() bool {
	return f.Exact != nil || f.GTE != nil || f.LTE != nil || f.GT != nil || f.LT != nil
}

// matches mirrors the inline comparison logic shared by
// service._apply_filing_filters, financial_metrics's date filter, and
// institutional_holdings._matches.
func (f dateFilters) matches(day time.Time) bool {
	if f.Exact != nil && !day.Equal(*f.Exact) {
		return false
	}
	if f.GTE != nil && day.Before(*f.GTE) {
		return false
	}
	if f.LTE != nil && day.After(*f.LTE) {
		return false
	}
	if f.GT != nil && !day.After(*f.GT) {
		return false
	}
	if f.LT != nil && !day.Before(*f.LT) {
		return false
	}
	return true
}

// buildDateFilters mirrors service._date_filters: validates the five
// exact/gte/lte/gt/lt string args under one field-name prefix (e.g.
// "report_period" -> report_period, report_period_gte, ...).
func buildDateFilters(exact, gte, lte, gt, lt *string, prefix string) (dateFilters, error) {
	var out dateFilters
	var err error
	if out.Exact, err = validateDate(exact, prefix); err != nil {
		return dateFilters{}, err
	}
	if out.GTE, err = validateDate(gte, prefix+"_gte"); err != nil {
		return dateFilters{}, err
	}
	if out.LTE, err = validateDate(lte, prefix+"_lte"); err != nil {
		return dateFilters{}, err
	}
	if out.GT, err = validateDate(gt, prefix+"_gt"); err != nil {
		return dateFilters{}, err
	}
	if out.LT, err = validateDate(lt, prefix+"_lt"); err != nil {
		return dateFilters{}, err
	}
	return out, nil
}

// FilingIdentity mirrors fd.py's FilingIdentity dataclass: filing metadata
// joined onto a statement or financial-metrics record when the filings join
// succeeded and a matching filing exists for that report_period.
type FilingIdentity struct {
	AccessionNumber *string
	FormType        *string
	FilingURL       *string
	FilingDate      *time.Time
}

// --- Generic provider-payload unwrapping (find_company) ---
//
// extractGenericRecords and firstStringGeneric duplicate the shape of
// go/providers' unexported extractRecords/firstStringPtr helpers (see
// filings.go): normalize.py's shared _extract_records/_first_string are not
// exported from the providers package, so find_company (the one caller
// service.py has that providers/filings.go's copy does not already cover)
// gets its own small copy here, scoped to this one call site.

// extractGenericRecords finds the record list nested under one of keys (or
// the payload itself, if already a list), mirroring
// normalize._extract_records.
func extractGenericRecords(value any, keys []string) ([]map[string]any, error) {
	current := value
	for i := 0; i < 5; i++ {
		if arr, ok := current.([]any); ok {
			records := make([]map[string]any, 0, len(arr))
			for index, item := range arr {
				obj, ok := item.(map[string]any)
				if !ok {
					return nil, &providers.SchemaDriftError{Msg: fmt.Sprintf("records[%d] is not an object", index)}
				}
				records = append(records, obj)
			}
			return records, nil
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
				records := make([]map[string]any, 0, len(arr))
				for index, item := range arr {
					childObj, ok := item.(map[string]any)
					if !ok {
						return nil, &providers.SchemaDriftError{Msg: fmt.Sprintf("%s[%d] is not an object", key, index)}
					}
					records = append(records, childObj)
				}
				return records, nil
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
	return nil, &providers.SchemaDriftError{Msg: "provider payload omitted the expected record list"}
}

// firstStringGeneric returns the first key present in record among keys
// whose value is a non-empty string, mirroring normalize._first_string.
func firstStringGeneric(record map[string]any, keys ...string) *string {
	for _, key := range keys {
		if v, ok := record[key].(string); ok && v != "" {
			return &v
		}
	}
	return nil
}

// findCompanyName looks up one ticker's company name in a DefiLlama
// /equities/v1/companies-list payload, mirroring the subset of
// normalize.find_company that service.get_company_facts actually uses
// (name only; sector/industry/exchange are always omitted, matching
// service.py's own company_facts_response(..., sector=None, industry=None,
// exchange=None) call). found reports whether a US record matched ticker
// at all (false -> the caller renders a not_found FD error); when found is
// true, name may still be nil if the matched record itself omits a name
// field (that is a normal, successful response with name omitted, NOT a
// not_found case - Python's find_company returns the whole record or
// None, and only a None record is not_found).
func findCompanyName(raw json.RawMessage, ticker string) (name *string, found bool, err error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false, &providers.SchemaDriftError{Msg: "provider payload is not valid JSON"}
	}
	records, err := extractGenericRecords(value, []string{"companies", "results", "data"})
	if err != nil {
		return nil, false, err
	}
	for _, record := range records {
		symbol := firstStringGeneric(record, "ticker", "symbol")
		country := firstStringGeneric(record, "country", "countryCode", "country_code")
		if symbol == nil || !strings.EqualFold(*symbol, ticker) {
			continue
		}
		if country != nil && !strings.EqualFold(*country, "US") {
			continue
		}
		return firstStringGeneric(record, "name", "companyName", "company_name"), true, nil
	}
	return nil, false, nil
}

// parseOptDate parses the first 10 characters of value as YYYY-MM-DD,
// returning nil (not an error) on any parse failure, mirroring
// segmented_financials._opt_date's try/except ValueError -> None.
func parseOptDate(value string) *time.Time {
	text := value
	if len(text) > 10 {
		text = text[:10]
	}
	t, err := time.Parse(dateLayout, text)
	if err != nil {
		return nil
	}
	return &t
}
