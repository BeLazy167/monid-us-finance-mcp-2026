// This file holds the tool table (all 27 Financial Datasets tool names
// mapped to their handlers) and the generic argument-coercion helpers every
// handler in service.go uses to read args map[string]any: the loose,
// JSON-decoded shape both the MCP and REST callers hand to Service.Call.
//
// Every handler validates and applies its tool's Financial Datasets JSON
// Schema default (docs/fd-mcp-tool-schemas.json) BEFORE any Monid call, so
// a bad request never costs the caller. Where the embedded Python service
// layer's own internal default differs from the published FD schema
// default (get_income_statement/get_balance_sheet/get_cash_flow_statement:
// Python defaults period to "annual", the FD schema declares "ttm";
// get_financial_metrics: same), this port follows the FD schema default,
// per this port's explicit brief.
package service

import (
	"fmt"

	"github.com/belazy/monid-finance/providers"
)

// handlerFunc runs one Financial Datasets tool call.
type handlerFunc func(*callCtx, map[string]any) (Result, error)

// toolHandlers maps all 27 Financial Datasets tool names to their handler.
var toolHandlers = map[string]handlerFunc{
	"get_company_facts":               (*callCtx).getCompanyFacts,
	"get_income_statement":            (*callCtx).getIncomeStatement,
	"get_balance_sheet":               (*callCtx).getBalanceSheet,
	"get_cash_flow_statement":         (*callCtx).getCashFlowStatement,
	"get_financial_metrics_snapshot":  (*callCtx).getFinancialMetricsSnapshot,
	"get_filings":                     (*callCtx).getFilings,
	"get_stock_prices":                (*callCtx).getStockPrices,
	"get_stock_price":                 (*callCtx).getStockPrice,
	"get_news":                        (*callCtx).getNews,
	"get_earnings":                    (*callCtx).getEarnings,
	"get_financial_metrics":           (*callCtx).getFinancialMetrics,
	"get_insider_trades":              (*callCtx).getInsiderTrades,
	"screen_stocks":                   (*callCtx).screenStocks,
	"list_stock_screener_filters":     (*callCtx).listStockScreenerFilters,
	"get_filing_items":                (*callCtx).getFilingItems,
	"list_filing_item_types":          (*callCtx).listFilingItemTypes,
	"get_segmented_financials":        (*callCtx).getSegmentedFinancials,
	"get_kpi_metrics":                 (*callCtx).getKPIMetrics,
	"get_kpi_guidance":                (*callCtx).getKPIGuidance,
	"get_kpi_non_gaap":                (*callCtx).getKPINonGAAP,
	"get_interest_rates":              (*callCtx).getInterestRates,
	"get_index_fund":                  (*callCtx).getIndexFund,
	"get_institutional_holdings":      (*callCtx).getInstitutionalHoldings,
	"get_beneficial_owners":           notImplementedHandler,
	"get_beneficial_ownership":        notImplementedHandler,
	"get_insider_ownership":           notImplementedHandler,
	"get_institutional_investors":     notImplementedHandler,
}

// notImplementedMessage mirrors server.py's NOT_IMPLEMENTED text exactly.
const notImplementedMessage = "This Financial Datasets tool is not implemented by the Monid-backed " +
	"server yet; the call was free and no data was fabricated."

// notImplementedHandler answers the four ownership-state tools that
// service.py never implemented (get_beneficial_owners,
// get_beneficial_ownership, get_insider_ownership,
// get_institutional_investors): a typed, zero-cost rejection with no Monid
// call and no ledger row, mirroring server.py's hardcoded
// {"error": "not_implemented", ...} stub for these four tools exactly
// (see NOT_IMPLEMENTED in server.py). *providers.UnsupportedError is the
// closest fit in this port's error taxonomy: the request is well formed
// but this server cannot honestly answer it yet.
func notImplementedHandler(_ *callCtx, _ map[string]any) (Result, error) {
	return Result{}, &providers.UnsupportedError{Msg: notImplementedMessage}
}

// --- generic argument coercion ---
//
// args is the loose map[string]any both MCP and REST callers hand to
// Service.Call (typically the result of decoding a caller's JSON request
// body/params). A value present under the wrong Go type is treated as an
// InputError naming the field, mirroring the type errors a strict JSON
// Schema validator would raise before this port's own business-logic
// validation ever runs.

func argRaw(args map[string]any, key string) (any, bool) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, false
	}
	return v, true
}

// argString reads an optional string argument.
func argString(args map[string]any, key string) (*string, error) {
	v, ok := argRaw(args, key)
	if !ok {
		return nil, nil
	}
	s, ok := v.(string)
	if !ok {
		return nil, &providers.InputError{Msg: key + " must be a string"}
	}
	return &s, nil
}

// argStringDefault reads a string argument, applying def when absent.
func argStringDefault(args map[string]any, key, def string) (string, error) {
	p, err := argString(args, key)
	if err != nil {
		return "", err
	}
	if p == nil {
		return def, nil
	}
	return *p, nil
}

// argFloat reads a JSON number argument (encoding/json decodes every JSON
// number into float64 when unmarshaled into `any`).
func argFloat(args map[string]any, key string) (*float64, error) {
	v, ok := argRaw(args, key)
	if !ok {
		return nil, nil
	}
	f, ok := v.(float64)
	if !ok {
		return nil, &providers.InputError{Msg: key + " must be a number"}
	}
	return &f, nil
}

// argInt reads an optional integer-valued argument.
func argInt(args map[string]any, key string) (*int, error) {
	f, err := argFloat(args, key)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, nil
	}
	if *f != float64(int(*f)) {
		return nil, &providers.InputError{Msg: key + " must be an integer"}
	}
	v := int(*f)
	return &v, nil
}

// argIntDefault reads an integer argument, applying def when absent.
func argIntDefault(args map[string]any, key string, def int) (int, error) {
	p, err := argInt(args, key)
	if err != nil {
		return 0, err
	}
	if p == nil {
		return def, nil
	}
	return *p, nil
}

// argBoolDefault reads a boolean argument, applying def when absent.
func argBoolDefault(args map[string]any, key string, def bool) (bool, error) {
	v, ok := argRaw(args, key)
	if !ok {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, &providers.InputError{Msg: key + " must be a boolean"}
	}
	return b, nil
}

// argStringSlice reads an optional array-of-strings argument.
func argStringSlice(args map[string]any, key string) ([]string, error) {
	v, ok := argRaw(args, key)
	if !ok {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, &providers.InputError{Msg: key + " must be an array of strings"}
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, &providers.InputError{Msg: key + " must be an array of strings"}
		}
		out = append(out, s)
	}
	return out, nil
}

// argObjectSlice reads an optional array-of-objects argument, reporting
// whether the key was present at all (present-but-empty and absent both
// decode to a nil slice, but callers like screen_stocks must tell "filters
// omitted" apart from "filters: []").
func argObjectSlice(args map[string]any, key string) ([]map[string]any, bool, error) {
	v, ok := argRaw(args, key)
	if !ok {
		return nil, false, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, true, &providers.InputError{Msg: key + " must be an array of objects"}
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, true, &providers.InputError{Msg: fmt.Sprintf("%s items must be objects", key)}
		}
		out = append(out, obj)
	}
	return out, true, nil
}
