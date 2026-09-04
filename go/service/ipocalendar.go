// This file builds getIPOCalendar: the upcoming-IPO calendar backed by
// stockanalysis's /get_ipo_calendar route (verified live, $0.01/call). It
// does not map to a real Financial Datasets MCP tool name, so it is
// reachable only through Service.GetIPOCalendar (capabilities.go), not
// toolHandlers.
//
// The live envelope is {"status":"success","data":{"thisWeek":[...],
// "nextWeek":[...],"later":[...]}}; when measured live, all three arrays
// were empty. That is a legitimate result (there just happened to be no
// IPOs pending in any bucket that day), not an error, so an empty payload
// answers an empty list rather than a schema-drift failure. Records map
// onto the Financial Datasets Ipo contract field names
// (docs/fd-contract-reference.json) as far as this route's fields let this
// port source them; ExpectedDate/Bucket are not part of that contract
// (Ipo is filing-centric: accession_number/cik/form_type/filing_date, not
// a calendar date), but genuinely sourced and worth keeping rather than
// discarding, so they are appended after the contract fields.
package service

import (
	"encoding/json"

	"github.com/belazy/monid-finance/providers"
)

const (
	stockanalysis       = "stockanalysis"
	ipoCalendarEndpoint = "/get_ipo_calendar"
	ipoCalendarMaxLimit = 500
)

// ipoCalendarBuckets is the live-verified bucket order this route reports,
// paired with the flat calendar bucket label each entry keeps.
var ipoCalendarBuckets = []struct{ key, bucket string }{
	{"thisWeek", "this_week"},
	{"nextWeek", "next_week"},
	{"later", "later"},
}

// ipoRecord is one IPO calendar entry for getIPOCalendar.
type ipoRecord struct {
	CompanyName           *string  `json:"company_name,omitempty"`
	Ticker                *string  `json:"ticker,omitempty"`
	Exchange              *string  `json:"exchange,omitempty"`
	ExpectedOfferingPrice *float64 `json:"expected_offering_price,omitempty"`
	PriceRangeLow         *float64 `json:"price_range_low,omitempty"`
	PriceRangeHigh        *float64 `json:"price_range_high,omitempty"`
	Status                *string  `json:"status,omitempty"`
	// Not part of the Financial Datasets Ipo contract; see the file doc
	// comment above.
	ExpectedDate *string `json:"expected_date,omitempty"`
	Bucket       *string `json:"bucket,omitempty"`
}

// getIPOCalendar answers getIPOCalendar: the flattened thisWeek/nextWeek/
// later calendar, newest bucket first, each entry tagged with its source
// bucket. limit is optional and defensive (this route's brief gave no
// schema for it); it defaults to no cap beyond ipoCalendarMaxLimit.
func (c *callCtx) getIPOCalendar(args map[string]any) (Result, error) {
	limitRaw, err := argIntDefault(args, "limit", ipoCalendarMaxLimit)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, ipoCalendarMaxLimit)
	if err != nil {
		return Result{}, err
	}

	run, err := c.run(stockanalysis, ipoCalendarEndpoint, nil, nil)
	if err != nil {
		return Result{}, err
	}
	var value any
	if err := json.Unmarshal(run.Output, &value); err != nil {
		return Result{}, &providers.SchemaDriftError{Msg: "stockanalysis IPO calendar payload must be valid JSON"}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Result{}, &providers.SchemaDriftError{Msg: "stockanalysis IPO calendar payload must be an object"}
	}
	if status, _ := root["status"].(string); status != "success" {
		return Result{}, &providers.SchemaDriftError{Msg: "stockanalysis IPO calendar status must be 'success'"}
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return Result{}, &providers.SchemaDriftError{Msg: "stockanalysis IPO calendar omitted a data object"}
	}

	var records []ipoRecord
	for _, b := range ipoCalendarBuckets {
		child, ok := data[b.key]
		if !ok {
			continue // a missing bucket is empty, not an error
		}
		items, ok := child.([]any)
		if !ok {
			return Result{}, &providers.SchemaDriftError{Msg: "stockanalysis IPO calendar bucket " + b.key + " must be an array"}
		}
		for _, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				return Result{}, &providers.SchemaDriftError{Msg: "stockanalysis IPO calendar entry must be an object"}
			}
			bucket := b.bucket
			record := ipoRecord{Bucket: &bucket}
			record.CompanyName = firstStringGeneric(row, "companyName", "company_name", "company", "name")
			record.Ticker = firstStringGeneric(row, "ticker", "symbol")
			record.Exchange = firstStringGeneric(row, "exchange", "market")
			record.Status = firstStringGeneric(row, "status")
			record.ExpectedDate = firstStringGeneric(row, "ipoDate", "expectedDate", "date", "priceDate")
			if priceRaw := firstStringGeneric(row, "expectedPrice", "offerPrice", "price"); priceRaw != nil {
				if v, ok := parseCommaFloat(stripLeadingDollar(*priceRaw)); ok {
					record.ExpectedOfferingPrice = &v
				}
			}
			if lowRaw := firstStringGeneric(row, "priceRangeLow", "priceLow"); lowRaw != nil {
				if v, ok := parseCommaFloat(stripLeadingDollar(*lowRaw)); ok {
					record.PriceRangeLow = &v
				}
			}
			if highRaw := firstStringGeneric(row, "priceRangeHigh", "priceHigh"); highRaw != nil {
				if v, ok := parseCommaFloat(stripLeadingDollar(*highRaw)); ok {
					record.PriceRangeHigh = &v
				}
			}
			records = append(records, record)
		}
	}
	if len(records) > limit {
		records = records[:limit]
	}

	out := make([]any, len(records))
	for i, r := range records {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "ipos"}, nil
}
