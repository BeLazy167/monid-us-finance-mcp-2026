// This file builds get_all_financials: income statement, balance sheet,
// and cash flow statement for one ticker, composed into a single
// {"financials": {...}} response. It does not map to a real Financial
// Datasets MCP tool name, so it is reachable only through
// Service.GetAllFinancials (capabilities.go), not toolHandlers.
package service

import (
	"sync"

	"github.com/belazy/monid-finance/monid"
)

// getAllFinancials composes income/balance/cash into one response, honoring
// the same period/limit/report_period(*)/filing_date(*) filters as the
// three individual statement routes (parseStatementArgs, statementResponse).
//
// Per this port's brief, "run the three fetches concurrently, the way the
// existing fan-out paths do": income, balance, and cash all live inside the
// SAME /equities/v1/statements payload (see statements.go's
// statementSections), so fetching that endpoint three times concurrently
// for one ticker would race three cache misses into three real (paid)
// Monid runs instead of one - a straight 3x overcharge for identical data.
// Instead, this fetches statements+filings ONCE, concurrently
// (concurrent2, mirroring statementResponse/getFinancialMetrics/
// earningsForTicker), and fans the three per-kind record builds for that
// one payload out over three goroutines - concurrent the way the brief
// asks, without tripling the bill.
func (c *callCtx) getAllFinancials(args map[string]any) (Result, error) {
	if err := checkAsReported(args); err != nil {
		return Result{}, err
	}
	parsed, err := parseStatementArgs(args, "ttm", 4, 100)
	if err != nil {
		return Result{}, err
	}

	var statementsRun *monid.Run
	var statementsErr error
	var filingsRun *monid.Run
	var filingsErr error
	concurrent2(
		func() {
			statementsRun, statementsErr = c.run(defillama, statementsEndpoint, nil,
				map[string]any{"ticker": parsed.ticker, "country": "US"})
		},
		func() {
			filingsRun, filingsErr = c.run(defillama, filingsEndpoint, nil,
				map[string]any{"ticker": parsed.ticker, "country": "US"})
		},
	)
	if statementsErr != nil {
		return Result{}, statementsErr
	}
	value, err := unmarshalRun(statementsRun)
	if err != nil {
		return Result{}, err
	}
	identityMap, err := buildFilingIdentityMap(filingsRun, filingsErr, parsed.ticker, identityForms(parsed.period))
	if err != nil {
		return Result{}, err
	}

	kinds := [3]string{"income", "balance", "cash"}
	results := [3][]any{}
	errs := [3]error{}
	var wg sync.WaitGroup
	wg.Add(len(kinds))
	for i, kind := range kinds {
		go func(i int, kind string) {
			defer wg.Done()
			results[i], errs[i] = c.statementRecords(kind, parsed, value, identityMap)
		}(i, kind)
	}
	wg.Wait()
	for _, kerr := range errs {
		if kerr != nil {
			return Result{}, kerr
		}
	}

	financials := newOrderedJSONObject()
	financials.set("income_statements", results[0])
	financials.set("balance_sheets", results[1])
	financials.set("cash_flow_statements", results[2])
	return Result{Value: map[string]any{"financials": financials}}, nil
}
