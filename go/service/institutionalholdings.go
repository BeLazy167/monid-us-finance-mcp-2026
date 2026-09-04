// This file ports institutional_holdings.py: mapping a SECForm4
// /get_institution_holders payload onto Financial Datasets
// InstitutionalHolding records for get_institutional_holdings.
package service

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

// institutionalHoldingsRowKeys mirrors institutional_holdings._ROW_KEYS.
var institutionalHoldingsRowKeys = []string{"results", "holders", "rows", "data", "table", "institutionHolders"}

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
		reportDay := instHoldingsFirstDate(row, "report_period", "reportDate", "date", "as_of", "periodEnd")
		if !instHoldingsMatches(reportDay, reportPeriod) {
			continue
		}
		filerName := firstStringGeneric(row, "filer_name", "institution", "holder", "manager", "name")
		shares := instHoldingsFirstInt(row, "shares", "shares_held", "num_shares", "position_shares", "current_shares")
		valueUSD := instHoldingsFirstInt(row, "value_usd", "market_value", "position_value", "current_value", "value")
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
		record.Shares = shares
		record.ValueUSD = valueUSD
		record.FilerName = filerName
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
