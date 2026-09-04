// This file ports institutional_holdings.py: mapping a SECForm4
// /get_institution_holders payload onto Financial Datasets
// InstitutionalHolding records for get_institutional_holdings.
package service

import (
	"encoding/json"
	"html"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

// institutionalHoldingsRowKeys names every key the holder table has been
// seen under. "tableData" is the one SECForm4 actually uses, verified live
// 2026-09-04 against cik=320193; the rest are retained as tolerated
// aliases so a shape change degrades to a different key rather than a
// hard failure.
var institutionalHoldingsRowKeys = []string{"tableData", "results", "holders", "rows", "data", "table", "institutionHolders"}

// holderAnchorText pulls the display text out of SECForm4's HTML "holder"
// cell, whose live value looks like:
//
//	<a href="/portfolio-holdings/1364742.html">BlackRock, Inc.</a><BR><a ...>
//
// The first anchor's text is the filer name. Anything unparseable yields
// no name rather than a mangled one.
var holderAnchorText = regexp.MustCompile(`<a[^>]*>([^<]+)</a>`)

// holderCIKHref pulls the filer's CIK out of that same cell's portfolio
// link (/portfolio-holdings/<cik>.html), which is the only place this feed
// reports it.
var holderCIKHref = regexp.MustCompile(`/portfolio-holdings/(\d+)`)

// holderName extracts the filer name from either a plain string or the
// HTML cell SECForm4 sends.
func holderName(raw string) *string {
	if !strings.Contains(raw, "<") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return nil
		}
		return &trimmed
	}
	if m := holderAnchorText.FindStringSubmatch(raw); m != nil {
		name := strings.TrimSpace(html.UnescapeString(m[1]))
		if name != "" {
			return &name
		}
	}
	return nil
}

// holderCIK extracts the filer CIK from the holder cell's portfolio link.
func holderCIK(raw string) *string {
	if m := holderCIKHref.FindStringSubmatch(raw); m != nil {
		// The portfolio link carries the CIK unpadded (1364742); every
		// other CIK this API emits is the SEC's ten-digit form.
		cik := strings.Repeat("0", cikDigits-len(m[1])) + m[1]
		return &cik
	}
	return nil
}

const cikDigits = 10

var instHoldingsQuarter = regexp.MustCompile(`^(\d{4})Q([1-4])$`)

// instHoldingsQuarterEnd reads the feed's "2026Q2" quarter label as the
// last day of that calendar quarter, which is the report_period of a 13F.
func instHoldingsQuarterEnd(row map[string]any) *time.Time {
	label, ok := row["quarter"].(string)
	if !ok {
		return nil
	}
	m := instHoldingsQuarter.FindStringSubmatch(label)
	if m == nil {
		return nil
	}
	// The pattern admits digits only, so Atoi cannot fail here.
	year, _ := strconv.Atoi(m[1])
	quarter, _ := strconv.Atoi(m[2])
	end := time.Date(year, time.Month(quarter*3)+1, 0, 0, 0, 0, 0, time.UTC)
	return &end
}

// instHoldingsRoundedInt reads a money-like field that the feed reports as
// a non-integer float (value came back as 336524794269.04004 live), which
// instHoldingsFirstInt rejects outright. Dollar values are rounded to the
// nearest unit rather than dropped.
func instHoldingsRoundedInt(row map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		f, ok := row[key].(float64)
		if !ok {
			continue
		}
		v := int64(math.Round(f))
		return &v
	}
	return nil
}

// normalizeInstitutionalHoldings mirrors
// institutional_holdings.normalize_institutional_holdings +
// fd.institutional_holding_record, sorted by value_usd descending (rows
// without a value_usd sort last) and truncated to limit.
func normalizeInstitutionalHoldings(raw json.RawMessage, ticker string, limit int, reportPeriod dateFilters) ([]fd.InstitutionalHolding, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 institution holders payload must be an object"}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 institution holders payload must be an object"}
	}
	if status, _ := root["status"].(string); status != "success" {
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 institution holders status must be 'success'"}
	}
	dataValue := root["data"]
	var rows []map[string]any
	var issuerName *string
	switch data := dataValue.(type) {
	case []any:
		list, err := instHoldingsObjectList(data, "SECForm4 institution holders rows")
		if err != nil {
			return nil, err
		}
		rows = list
	case map[string]any:
		issuerName = firstStringGeneric(data, "name_of_issuer", "companyName", "company", "issuer")
		list, err := instHoldingsRows(data)
		if err != nil {
			return nil, err
		}
		rows = list
	default:
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 institution holders omitted a data object"}
	}

	type scored struct {
		record   fd.InstitutionalHolding
		hasValue bool
		valueUSD int64
	}
	var records []scored
	for _, row := range rows {
		// SECForm4 labels each row with the 13F quarter ("2026Q2") and the
		// moment the filer submitted it ("2026-08-07<BR>12:29:42"). The
		// quarter end is the report_period. The submission is the
		// filing_date; it used to be read as report_period, six weeks late.
		reportDay := instHoldingsQuarterEnd(row)
		if reportDay == nil {
			reportDay = instHoldingsFirstDate(row, "report_period", "reportDate", "date", "as_of", "periodEnd")
		}
		if !instHoldingsMatches(reportDay, reportPeriod) {
			continue
		}
		filingDay := instHoldingsFirstDate(row, "report_date_time", "filing_date", "filed")
		// SECForm4 sends the filer as an HTML cell carrying both the name
		// and, in its portfolio link, the filer CIK. Everything else in
		// this feed is a plain value.
		var filerName, filerCIK *string
		if raw := firstStringGeneric(row, "filer_name", "institution", "holder", "manager", "name"); raw != nil {
			filerName = holderName(*raw)
			filerCIK = holderCIK(*raw)
		}
		shares := instHoldingsFirstInt(row, "shares", "shares_held", "num_shares", "position_shares", "current_shares", "newShares")
		valueUSD := instHoldingsFirstInt(row, "value_usd", "market_value", "position_value", "current_value")
		if valueUSD == nil {
			valueUSD = instHoldingsRoundedInt(row, "value")
		}
		rowIssuer := firstStringGeneric(row, "name_of_issuer", "company", "issuer")
		if filerName == nil && shares == nil && valueUSD == nil {
			continue
		}
		nameOfIssuer := rowIssuer
		if nameOfIssuer == nil {
			nameOfIssuer = issuerName
		}
		tickerCopy := ticker
		record := fd.InstitutionalHolding{Ticker: &tickerCopy}
		record.NameOfIssuer = nameOfIssuer
		if reportDay != nil {
			s := reportDay.Format(dateLayout)
			record.ReportPeriod = &s
		}
		if filingDay != nil {
			s := filingDay.Format(dateLayout)
			record.FilingDate = &s
		}
		record.Shares = shares
		record.ValueUSD = valueUSD
		record.FilerName = filerName
		record.FilerCIK = filerCIK
		s := scored{record: record}
		if valueUSD != nil {
			s.hasValue = true
			s.valueUSD = *valueUSD
		}
		records = append(records, s)
	}
	// Sort by (has value_usd, value_usd) descending, mirroring the Python
	// sort key `(isinstance(value_usd, int), value_usd or 0)` under
	// reverse=True (a stable sort, so equal keys keep source order).
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].hasValue != records[j].hasValue {
			return records[i].hasValue
		}
		if !records[i].hasValue {
			return false
		}
		return records[i].valueUSD > records[j].valueUSD
	})
	if limit >= 0 && len(records) > limit {
		records = records[:limit]
	}
	out := make([]fd.InstitutionalHolding, 0, len(records))
	for _, r := range records {
		out = append(out, r.record)
	}
	return out, nil
}

func instHoldingsRows(data map[string]any) ([]map[string]any, error) {
	for _, key := range institutionalHoldingsRowKeys {
		if child, ok := data[key].([]any); ok {
			return instHoldingsObjectList(child, "SECForm4 holder "+key)
		}
	}
	return nil, &providers.SchemaDriftError{Msg: "SECForm4 institution holders omitted the holder table"}
}

func instHoldingsObjectList(values []any, name string) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(values))
	for _, item := range values {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, &providers.SchemaDriftError{Msg: name + " row must be an object"}
		}
		rows = append(rows, obj)
	}
	return rows, nil
}

func instHoldingsMatches(day *time.Time, filters dateFilters) bool {
	if !filters.any() {
		return true
	}
	if day == nil {
		return false
	}
	return filters.matches(*day)
}

func instHoldingsFirstDate(row map[string]any, keys ...string) *time.Time {
	for _, key := range keys {
		s, ok := row[key].(string)
		if !ok || s == "" {
			continue
		}
		text := s
		if len(text) > 10 {
			text = text[:10]
		}
		if t, err := time.Parse(dateLayout, text); err == nil {
			return &t
		}
	}
	return nil
}

func instHoldingsFirstInt(row map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		value, ok := row[key]
		if !ok {
			continue
		}
		if _, isBool := value.(bool); isBool {
			continue
		}
		if f, ok := value.(float64); ok && f == float64(int64(f)) {
			v := int64(f)
			return &v
		}
	}
	return nil
}
