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
package service

import (
	"encoding/json"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

const (
	nasdaqMarketMoversEndpoint = "/get_market_movers"
)

// marketMoverRow is one row of nasdaq's most-active-by-share-volume table.
type marketMoverRow struct {
	Symbol         *string  `json:"symbol,omitempty"`
	Name           *string  `json:"name,omitempty"`
	LastSalePrice  *float64 `json:"last_sale_price,omitempty"`
	LastSaleChange *float64 `json:"last_sale_change,omitempty"`
	ShareVolume    *int64   `json:"share_volume,omitempty"`
}

// marketSnapshot is getMarketSnapshot's response shape.
type marketSnapshot struct {
	DataAsOf                *string          `json:"data_as_of,omitempty"`
	LastTradeTimestamp      *string          `json:"last_trade_timestamp,omitempty"`
	MostActiveByShareVolume []marketMoverRow `json:"most_active_by_share_volume,omitempty"`
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
	snapshot.LastTradeTimestamp = firstStringGeneric(mostActive, "lastTradeTimestamp")

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
		out.Symbol = firstStringGeneric(row, "symbol")
		out.Name = firstStringGeneric(row, "name")
		if priceRaw := firstStringGeneric(row, "lastSalePrice"); priceRaw != nil {
			if v, ok := parseCommaFloat(stripLeadingDollar(*priceRaw)); ok {
				out.LastSalePrice = &v
			}
		}
		if changeRaw := firstStringGeneric(row, "lastSaleChange"); changeRaw != nil {
			cleaned := strings.TrimPrefix(stripLeadingDollar(*changeRaw), "+")
			if v, ok := parseCommaFloat(cleaned); ok {
				out.LastSaleChange = &v
			}
		}
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
	snapshot.MostActiveByShareVolume = rows

	return Result{Value: snapshot}, nil
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
