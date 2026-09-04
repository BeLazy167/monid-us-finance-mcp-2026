// This file builds search_line_items: an arbitrary set of income
// statement / balance sheet / cash flow field names, searched across an
// arbitrary (small, capped) set of tickers. It does not map to a real
// Financial Datasets MCP tool name, so it is reachable only through
// Service.SearchLineItems (capabilities.go), not toolHandlers.
package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/belazy/monid-finance/providers"
)

// maxSearchLineItemsTickers caps how many tickers one search_line_items
// call can fan out to. Each ticker is its own paid
// /equities/v1/statements call, so without a cap one request could turn
// into an unbounded spend; 5 is a sane starting point per this port's
// brief.
const maxSearchLineItemsTickers = 5

// searchLineItems reuses the same statement-matrix parsing
// get_income_statement/get_balance_sheet/get_cash_flow_statement build on
// (buildStatementRecords), fetching all three statement kinds for every
// requested ticker and returning only the caller's requested field names,
// merged one row per (ticker, report_period). limit bounds how many
// report periods each ticker contributes (most recent first), matching
// every other statement tool's own "most recent N periods" limit
// semantics - Financial Datasets' own schema does not say whether limit is
// per-ticker or global, and per-ticker is what every other limit in this
// server means.
func (c *callCtx) searchLineItems(args map[string]any) (Result, error) {
	lineItems, err := argStringSlice(args, "line_items")
	if err != nil {
		return Result{}, err
	}
	if len(lineItems) == 0 {
		return Result{}, &providers.InputError{Msg: "line_items is required and must include at least one entry"}
	}
	tickerArgs, err := argStringSlice(args, "tickers")
	if err != nil {
		return Result{}, err
	}
	if len(tickerArgs) == 0 {
		return Result{}, &providers.InputError{Msg: "tickers is required and must include at least one entry"}
	}
	if len(tickerArgs) > maxSearchLineItemsTickers {
		return Result{}, &providers.InputError{Msg: fmt.Sprintf("tickers must include at most %d entries", maxSearchLineItemsTickers)}
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
	periodRaw, err := argStringDefault(args, "period", "ttm")
	if err != nil {
		return Result{}, err
	}
	period, err := validatePeriod(periodRaw)
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 1)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 100)
	if err != nil {
		return Result{}, err
	}

	// Fetch every ticker's statements payload concurrently: independent
	// Monid calls, so this mirrors every other N-way fan-out in this
	// package (getInterestRates, earningsFeed) rather than looping
	// sequentially.
	values := make([]any, len(tickers))
	errs := make([]error, len(tickers))
	var wg sync.WaitGroup
	wg.Add(len(tickers))
	for i, ticker := range tickers {
		go func(i int, ticker string) {
			defer wg.Done()
			run, rerr := c.run(defillama, statementsEndpoint, nil, map[string]any{"ticker": ticker, "country": "US"})
			if rerr != nil {
				errs[i] = rerr
				return
			}
			value, uerr := unmarshalRun(run)
			if uerr != nil {
				errs[i] = uerr
				return
			}
			values[i] = value
		}(i, ticker)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return Result{}, e
		}
	}

	parsed := statementArgs{period: period, limit: limit}
	var results []any
	for i, ticker := range tickers {
		parsed.ticker = ticker
		rows, rerr := c.mergedLineItemRows(parsed, values[i], wantsCashFlowFields(lineItems))
		if rerr != nil {
			return Result{}, rerr
		}
		for _, row := range rows {
			results = append(results, selectLineItems(ticker, period, row, lineItems))
		}
	}
	return Result{Value: results, WrapperKey: "search_results"}, nil
}

// lineItemRow is one report period's merged income+balance+cash fields
// for one ticker, keyed by Financial Datasets field name (e.g. "revenue",
// "total_assets", "free_cash_flow").
type lineItemRow struct {
	reportPeriod string
	fields       map[string]any
}

// cashFlowFieldsFromMarketbeat are the cash flow fields this server
// sources from marketbeat rather than the normalized feed (see
// marketbeatcashflow.go). Only a request touching one of them needs that
// extra paid call.
var cashFlowFieldsFromMarketbeat = map[string]bool{
	"net_income": true, "depreciation_and_amortization": true,
	"net_cash_flow_from_operations": true, "capital_expenditure": true,
	"net_cash_flow_from_investing": true, "net_cash_flow_from_financing": true,
	"dividends_and_other_cash_distributions": true,
	"change_in_cash_and_equivalents":         true, "free_cash_flow": true,
}

// wantsCashFlowFields reports whether a line-item request asks for
// anything the corrected cash flow source supplies.
//
// This gate exists because search_line_items fans out across many
// tickers, so routing every one through marketbeat unconditionally would
// multiply the bill for callers who only asked for revenue. A request
// that does touch a cash flow field pays for the correct number.
func wantsCashFlowFields(lineItems []string) bool {
	for _, item := range lineItems {
		if cashFlowFieldsFromMarketbeat[item] {
			return true
		}
	}
	return false
}

// mergedLineItemRows builds one ticker's merged rows for searchLineItems,
// reusing buildStatementRecords - the same row-filtering/limiting/record-
// building logic get_income_statement/get_balance_sheet/
// get_cash_flow_statement use - for each of the three statement kinds
// against an already-fetched statements payload, then merging
// same-report_period records into one field map per period. identityMap
// is always nil here: search_line_items has no filing_date filter in its
// Financial Datasets schema, so there is nothing to join filing identity
// for. A statement kind whose section is entirely absent from the payload
// is skipped (not fatal): a company may report income and balance data
// with no separately labeled cash-flow section, and the caller still gets
// whichever requested line items exist.
func (c *callCtx) mergedLineItemRows(parsed statementArgs, value any, correctedCashFlow bool) ([]lineItemRow, error) {
	merged := map[string]map[string]any{}
	for _, kind := range [3]string{"income", "balance", "cash"} {
		var records []any
		var err error
		if kind == "cash" && correctedCashFlow {
			records, err = c.statementRecords(kind, parsed, value, nil)
		} else {
			records, err = buildStatementRecords(kind, parsed, value, nil)
		}
		if err != nil {
			if _, ok := err.(*providers.SchemaDriftError); ok {
				continue
			}
			return nil, err
		}
		for _, record := range records {
			raw, merr := json.Marshal(record)
			if merr != nil {
				return nil, merr
			}
			var fields map[string]any
			if uerr := json.Unmarshal(raw, &fields); uerr != nil {
				return nil, uerr
			}
			reportPeriod, _ := fields["report_period"].(string)
			if reportPeriod == "" {
				continue
			}
			row, exists := merged[reportPeriod]
			if !exists {
				row = map[string]any{}
				merged[reportPeriod] = row
			}
			for k, v := range fields {
				if k == "ticker" || k == "report_period" || k == "period" {
					continue
				}
				row[k] = v
			}
		}
	}
	periods := make([]string, 0, len(merged))
	for reportPeriod := range merged {
		periods = append(periods, reportPeriod)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(periods)))
	if len(periods) > parsed.limit {
		periods = periods[:parsed.limit]
	}
	rows := make([]lineItemRow, len(periods))
	for i, reportPeriod := range periods {
		rows[i] = lineItemRow{reportPeriod: reportPeriod, fields: merged[reportPeriod]}
	}
	return rows, nil
}

// selectLineItems builds one search_results row: ticker, report_period,
// period, plus whichever of lineItems exist in row's merged field set, in
// the caller's requested order. currency is never set: DefiLlama's
// statements payload carries no currency field, and every statement
// builder in service.go (buildIncomeStatement, buildBalanceSheet,
// buildCashFlowStatement) already leaves fd's own Currency field unset
// for the same reason - this never fabricates one either.
func selectLineItems(ticker, period string, row lineItemRow, lineItems []string) *orderedJSONObject {
	out := newOrderedJSONObject()
	out.set("ticker", ticker)
	out.set("report_period", row.reportPeriod)
	out.set("period", period)
	for _, item := range lineItems {
		if v, ok := row.fields[item]; ok {
			out.set(item, v)
		}
	}
	return out
}
