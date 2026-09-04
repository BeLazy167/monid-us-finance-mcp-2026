// This file builds getMarketSnapshot from nasdaq's /get_market_movers
// route (verified live, $0.01/call): the exchange's most-active-by-share-
// volume list. Unlike the ownership-state feeds in this package, this data
// IS current: the live probe this port's brief was built against reported
// "Data as of Sep 4, 2026 2:18 PM ET", so this file carries that as-of
// timestamp through rather than attaching any staleness caveat.
//
// /get_market_indices was offered as an optional second source, but its
// payload shape was never independently verified the way movers' was (no
// field list, no live sample), so this port does not guess at it; adding
// a second concurrent call here would only be safe once that shape is
// confirmed. getMarketSnapshot therefore issues exactly one call today;
// wiring indices in later needs no shape change to this file's exported
// contract, only a second concurrent fetch inside this handler.
//
// The live payload nests as data.data.STOCKS.MostActiveByShareVolume,
// carrying dataAsOf, lastTradeTimestamp, and table.rows of
// {symbol, name, lastSalePrice, lastSaleChange, change}. lastSalePrice
// (and, empirically, lastSaleChange) are money strings with a leading "$";
// the row's "change" key is the confusingly-named share-volume figure
// (comma-grouped), not a price change - this file renames it
// share_volume in the output so that confusion does not propagate.
//
// Rows are emitted in the Financial Datasets PriceSnapshot contract shape
// (fd.PriceSnapshot), the same record /prices/snapshot already answers
// with, so /prices/snapshot/market matches Financial Datasets' own
// PriceSnapshotMarketResponse field-for-field. What differs from
// Financial Datasets is coverage, not shape: this snapshot carries
// nasdaq's most-active-by-share-volume set, not the whole market.
package service

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

const (
	nasdaqMarketMoversEndpoint = "/get_market_movers"
)

// marketMoverRow is one most-active row in the Financial Datasets
// PriceSnapshot contract shape, followed by the two figures nasdaq
// genuinely reports that the contract has no field for: the company name,
// and the share volume this feed actually ranks by. Both are appended
// after the contract fields rather than discarded, the same way ipoRecord
// appends expected_date/bucket and beneficialOwnershipRow appends
// share_change.
type marketMoverRow struct {
	fd.PriceSnapshot
	Name        *string `json:"name,omitempty"`
	ShareVolume *int64  `json:"share_volume,omitempty"`
}

// marketSnapshot is getMarketSnapshot's response shape: Financial
// Datasets' PriceSnapshotMarketResponse ({"snapshots": [...]}), plus
// data_as_of - the feed's own human-readable as-of label, kept so a
// caller can see exactly how current the snapshot is without parsing a
// record. Snapshots is not omitempty: a market with no movers must answer
// an empty list, never a missing key.
type marketSnapshot struct {
	DataAsOf  *string          `json:"data_as_of,omitempty"`
	Snapshots []marketMoverRow `json:"snapshots"`
}

// getMarketSnapshot answers getMarketSnapshot: nasdaq's current
// most-active-by-share-volume snapshot, with its own as-of timestamp
// carried through so callers see exactly how current it is.
func (c *callCtx) getMarketSnapshot(args map[string]any) (Result, error) {
	run, err := c.run(nasdaq, nasdaqMarketMoversEndpoint, nil, nil)
	if err != nil {
		return Result{}, err
	}
	var value any
	if err := json.Unmarshal(run.Output, &value); err != nil {
		return Result{}, &providers.SchemaDriftError{Msg: "nasdaq market movers payload must be valid JSON"}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Result{}, &providers.SchemaDriftError{Msg: "nasdaq market movers payload must be an object"}
	}
	if status, _ := root["status"].(string); status != "success" {
		return Result{}, &providers.SchemaDriftError{Msg: "nasdaq market movers status must be 'success'"}
	}
	mostActive, err := navigateMostActive(root)
	if err != nil {
		return Result{}, err
	}

	snapshot := marketSnapshot{}
	snapshot.DataAsOf = firstStringGeneric(mostActive, "dataAsOf")
	// Every row in one payload shares the feed's single trade timestamp,
	// so it is parsed once here and copied onto each record, where the
	// PriceSnapshot contract puts it.
	snapTime, snapMillis := parseSnapshotTime(firstStringGeneric(mostActive, "lastTradeTimestamp"))

	table, ok := mostActive["table"].(map[string]any)
	if !ok {
		return Result{}, &providers.SchemaDriftError{Msg: "nasdaq market movers omitted its rows table"}
	}
	rowsValue, ok := table["rows"].([]any)
	if !ok {
		return Result{}, &providers.SchemaDriftError{Msg: "nasdaq market movers table.rows must be an array"}
	}

	rows := make([]marketMoverRow, 0, len(rowsValue))
	for _, item := range rowsValue {
		row, ok := item.(map[string]any)
		if !ok {
			return Result{}, &providers.SchemaDriftError{Msg: "nasdaq market movers row must be an object"}
		}
		out := marketMoverRow{}
		out.Ticker = firstStringGeneric(row, "symbol")
		out.Name = firstStringGeneric(row, "name")
		out.Time = snapTime
		out.TimeMilliseconds = snapMillis
		if priceRaw := firstStringGeneric(row, "lastSalePrice"); priceRaw != nil {
			if v, ok := parseCommaFloat(stripLeadingDollar(*priceRaw)); ok {
				out.Price = &v
			}
		}
		if changeRaw := firstStringGeneric(row, "lastSaleChange"); changeRaw != nil {
			cleaned := strings.TrimPrefix(stripLeadingDollar(*changeRaw), "+")
			if v, ok := parseCommaFloat(cleaned); ok {
				out.DayChange = &v
			}
		}
		out.DayChangePercent = dayChangePercent(out.Price, out.DayChange)
		// The header map names this field "change"; it is share volume,
		// not a price change (see the file doc comment).
		if volumeRaw := firstStringGeneric(row, "change"); volumeRaw != nil {
			if v, ok := parseCommaFloat(*volumeRaw); ok && v == float64(int64(v)) {
				iv := int64(v)
				out.ShareVolume = &iv
			}
		}
		rows = append(rows, out)
	}
	snapshot.Snapshots = rows

	return Result{Value: snapshot}, nil
}

// parseSnapshotTime renders the feed's own trade timestamp as the
// PriceSnapshot contract's pair: time as an RFC3339 UTC instant (matching
// what /prices/snapshot already answers with) and time_milliseconds as
// the same instant in epoch milliseconds. The live-measured value is
// RFC3339 with an offset ("2026-09-04T14:18:00-04:00"); a value in any
// other format yields two omitted fields rather than a guessed instant.
func parseSnapshotTime(raw *string) (*string, *float64) {
	if raw == nil {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return nil, nil
	}
	formatted := parsed.UTC().Format(time.RFC3339)
	millis := float64(parsed.UnixMilli())
	return &formatted, &millis
}

// dayChangePercent derives the PriceSnapshot contract's day_change_percent
// from the two figures nasdaq does report. The previous close is price
// minus the day's change; a zero previous close has no defined percentage,
// so the field is omitted rather than reported as an infinity.
func dayChangePercent(price, dayChange *float64) *float64 {
	if price == nil || dayChange == nil {
		return nil
	}
	previousClose := *price - *dayChange
	if previousClose == 0 {
		return nil
	}
	pct := *dayChange / previousClose * 100
	return &pct
}

// navigateMostActive walks root.data.STOCKS.MostActiveByShareVolume,
// mirroring the live-verified data.data.STOCKS.MostActiveByShareVolume
// nesting (root's own top-level "data" key is the first "data" in that
// path; the second is the object this function returns).
func navigateMostActive(root map[string]any) (map[string]any, error) {
	data, ok := root["data"].(map[string]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "nasdaq market movers omitted a data object"}
	}
	inner, ok := data["data"].(map[string]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "nasdaq market movers omitted its inner data object"}
	}
	stocks, ok := inner["STOCKS"].(map[string]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "nasdaq market movers omitted STOCKS"}
	}
	mostActive, ok := stocks["MostActiveByShareVolume"].(map[string]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "nasdaq market movers omitted MostActiveByShareVolume"}
	}
	return mostActive, nil
}
