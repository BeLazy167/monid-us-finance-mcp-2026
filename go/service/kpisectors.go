// This file builds getKPISectors: batch ticker->sector resolution backed
// by finviz's /get_ticker_sectors_with_performance route (verified live,
// $0.01/call, up to 100 tickers per call). It does not map to a real
// Financial Datasets MCP tool name, so it is reachable only through
// Service.GetKPISectors (capabilities.go), not toolHandlers, the same way
// GetAllFinancials/SearchLineItems are - this is the capability
// docs/monid_finance_discovery.json's /kpi/metrics/sectors route needs so
// that route stops being a permanent notImplementedPaths stub once the
// REST layer wires it up.
package service

import (
	"fmt"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

const (
	finviz                = "finviz"
	tickerSectorsEndpoint = "/get_ticker_sectors_with_performance"
	maxKPISectorsTickers  = 100
)

// tickerSector is one ticker's resolved sector for getKPISectors.
// Performance carries whatever "with-performance" figure the route
// reports alongside the sector (e.g. a day change percent); it is omitted
// when the payload does not carry a recognizable one, never guessed.
type tickerSector struct {
	Ticker      *string  `json:"ticker,omitempty"`
	Sector      *string  `json:"sector,omitempty"`
	Performance *float64 `json:"performance,omitempty"`
}

// getKPISectors batch-resolves the sector (and, when present, a
// performance figure) for up to 100 tickers in one call.
func (c *callCtx) getKPISectors(args map[string]any) (Result, error) {
	tickerArgs, err := argStringSlice(args, "tickers")
	if err != nil {
		return Result{}, err
	}
	if len(tickerArgs) == 0 {
		return Result{}, &providers.InputError{Msg: "tickers is required and must include at least one entry"}
	}
	if len(tickerArgs) > maxKPISectorsTickers {
		return Result{}, &providers.InputError{Msg: fmt.Sprintf("tickers must include at most %d entries", maxKPISectorsTickers)}
	}
	tickers := make([]string, len(tickerArgs))
	seen := make(map[string]bool, len(tickerArgs))
	for i, raw := range tickerArgs {
		symbol, verr := validateTicker(raw)
		if verr != nil {
			return Result{}, verr
		}
		if seen[symbol] {
			return Result{}, &providers.InputError{Msg: "tickers must not repeat " + symbol}
		}
		seen[symbol] = true
		tickers[i] = symbol
	}

	tickersAny := make([]any, len(tickers))
	for i, t := range tickers {
		tickersAny[i] = t
	}
	run, err := c.run(finviz, tickerSectorsEndpoint, nil, map[string]any{"tickers": tickersAny})
	if err != nil {
		return Result{}, err
	}
	rows, err := statusEnvelopeRows(run.Output, "ticker sectors")
	if err != nil {
		return Result{}, err
	}

	byTicker := make(map[string]tickerSector, len(rows))
	for _, row := range rows {
		symbol := firstStringGeneric(row, "ticker", "symbol", "Ticker", "Symbol")
		if symbol == nil {
			continue
		}
		key := normalizeTickerKey(*symbol)
		entry := tickerSector{Ticker: symbol}
		entry.Sector = firstStringGeneric(row, "sector", "Sector")
		if perf, ok := row["performance"]; ok {
			if f, ok := perf.(float64); ok {
				entry.Performance = &f
			}
		} else if perf, ok := row["change"]; ok {
			if f, ok := perf.(float64); ok {
				entry.Performance = &f
			}
		}
		byTicker[key] = entry
	}

	out := make([]any, 0, len(tickers))
	for _, symbol := range tickers {
		if entry, ok := byTicker[symbol]; ok {
			out = append(out, entry)
			continue
		}
		s := symbol
		out = append(out, tickerSector{Ticker: &s})
	}
	return Result{Value: out, WrapperKey: "sectors"}, nil
}

// normalizeTickerKey upper-cases a ticker-ish provider value for map
// keying without validating its shape (the value came from provider
// output, not caller input, so it must not error here).
func normalizeTickerKey(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}
