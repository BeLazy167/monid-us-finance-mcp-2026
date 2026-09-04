// This file ports normalize.normalize_summary/normalize_stock_price plus
// service._as_ratio/_ratio and fd.price_snapshot_response/
// metric_snapshot_record: turning a DefiLlama /equities/v1/summary payload
// into an FD PriceSnapshot and FinancialMetricSnapshot.
package providers

import (
	"encoding/json"
	"math"

	"github.com/belazy/monid-finance/fd"
)

// summaryStatementKeys flags a payload as already being the statements root
// (rather than a summary payload wrapped once in "data"), mirroring
// normalize._unwrap_mapping's STATEMENT_KEYS guard.
var summaryStatementKeys = []string{
	"incomeStatement", "income_statement", "income",
	"balanceSheet", "balance_sheet", "balance",
	"cashflow", "cashFlow", "cash_flow", "cashFlowStatement",
}

// Summary is the parsed DefiLlama /equities/v1/summary payload, with every
// field resolved through its provider-specific aliases. Mirrors the dict
// normalize.normalize_summary returns.
type Summary struct {
	Price                    *float64
	Volume                   *float64
	MarketCap                *float64
	EnterpriseValue          *float64
	FiftyTwoWeekHigh         *float64
	FiftyTwoWeekLow          *float64
	DividendYield            *float64
	DayChange                *float64
	DayChangePercent         *float64
	PriceToEarnings          *float64
	PriceToRevenue           *float64
	PriceToBook              *float64
	EnterpriseValueToEBITDA  *float64
	RevenueTTM               *float64
	GrossProfitTTM           *float64
	NetIncomeTTM             *float64
	EBITDATTM                *float64
	OperatingProfitMarginTTM *float64
	Currency                 *string
	AsOf                     *string
}

// summaryNumericKeys are the fields normalize_summary requires at least one
// of, and validates as finite when present.
var summaryNumericKeys = []string{"price", "currentPrice", "volume", "marketCap", "trailingPE", "revenueTTM"}

func unwrapSummaryMapping(raw json.RawMessage) (map[string]any, error) {
	current, err := unmarshalAny(raw)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 4; i++ {
		obj, ok := current.(map[string]any)
		if !ok {
			break
		}
		hasStatementKey := false
		for _, key := range summaryStatementKeys {
			if _, exists := obj[key]; exists {
				hasStatementKey = true
				break
			}
		}
		if hasStatementKey {
			return obj, nil
		}
		nested, exists := obj["data"]
		if exists {
			if nestedObj, ok := nested.(map[string]any); ok {
				current = nestedObj
				continue
			}
		}
		return obj, nil
	}
	obj, ok := current.(map[string]any)
	if !ok {
		return nil, schemaDriftf("provider payload is not an object")
	}
	return obj, nil
}

func firstNumber(record map[string]any, keys ...string) *float64 {
	value := firstValue(record, keys...)
	if f, ok := numberValue(value); ok {
		return &f
	}
	return nil
}

// payloadAsOf reads the first present timestamp-shaped field, mirroring
// normalize.payload_as_of.
func payloadAsOf(record map[string]any) *string {
	for _, key := range []string{"timestamp", "updatedAt", "asOf", "as_of", "date"} {
		if s, ok := record[key].(string); ok && s != "" {
			return &s
		}
	}
	return nil
}

// ParseSummary parses a DefiLlama /equities/v1/summary payload, mirroring
// normalize.normalize_summary. It fails when every recognized numeric
// market field is absent or non-finite.
func ParseSummary(raw json.RawMessage) (Summary, error) {
	obj, err := unwrapSummaryMapping(raw)
	if err != nil {
		return Summary{}, err
	}
	anyFinite := false
	for _, key := range summaryNumericKeys {
		value, exists := obj[key]
		if !exists || value == nil {
			continue
		}
		if _, ok := numberValue(value); !ok {
			return Summary{}, schemaDriftf("DefiLlama summary field %s is not finite numeric data", key)
		}
		anyFinite = true
	}
	if len(obj) == 0 || !anyFinite {
		return Summary{}, schemaDriftf("DefiLlama summary omitted recognized numeric market fields")
	}
	return Summary{
		Price:                    firstNumber(obj, "price", "currentPrice"),
		Volume:                   firstNumber(obj, "volume"),
		MarketCap:                firstNumber(obj, "market_cap", "marketCap"),
		EnterpriseValue:          firstNumber(obj, "enterprise_value", "enterpriseValue"),
		FiftyTwoWeekHigh:         firstNumber(obj, "fiftyTwoWeekHigh"),
		FiftyTwoWeekLow:          firstNumber(obj, "fiftyTwoWeekLow"),
		DividendYield:            firstNumber(obj, "dividend_yield", "dividendYield"),
		DayChange:                firstNumber(obj, "priceChange1d"),
		DayChangePercent:         firstNumber(obj, "priceChangePercentage1d"),
		PriceToEarnings:          firstNumber(obj, "trailingPE", "peRatio"),
		PriceToRevenue:           firstNumber(obj, "priceToRevenue"),
		PriceToBook:              firstNumber(obj, "priceToBook"),
		EnterpriseValueToEBITDA:  firstNumber(obj, "enterpriseValueToEbitda"),
		RevenueTTM:               firstNumber(obj, "revenueTTM"),
		GrossProfitTTM:           firstNumber(obj, "grossProfitTTM"),
		NetIncomeTTM:             firstNumber(obj, "earningsTTM"),
		EBITDATTM:                firstNumber(obj, "ebitdaTTM"),
		OperatingProfitMarginTTM: firstNumber(obj, "operatingProfitMarginTTM"),
		Currency:                 firstStringPtr(obj, "currency", "currencyCode"),
		AsOf:                     payloadAsOf(obj),
	}, nil
}

// BuildPriceSnapshot builds the FD PriceSnapshot for one ticker, mirroring
// normalize.normalize_stock_price + fd.price_snapshot_response. It fails
// when the summary has no finite current price.
func BuildPriceSnapshot(ticker string, s Summary) (fd.PriceSnapshot, error) {
	if s.Price == nil {
		return fd.PriceSnapshot{}, schemaDriftf("DefiLlama summary omitted a finite numeric current price")
	}
	symbol := ticker
	price := *s.Price
	snapshot := fd.PriceSnapshot{Price: &price, Ticker: &symbol}
	if s.DayChange != nil {
		v := *s.DayChange
		snapshot.DayChange = &v
	}
	if s.DayChangePercent != nil {
		v := *s.DayChangePercent
		snapshot.DayChangePercent = &v
	}
	if s.AsOf != nil {
		v := *s.AsOf
		snapshot.Time = &v
	}
	return snapshot, nil
}

// ratio divides numerator by denominator, or nil if either is missing or
// the denominator is zero, mirroring service._ratio.
func ratio(numerator, denominator *float64) *float64 {
	if numerator == nil || denominator == nil || *denominator == 0 {
		return nil
	}
	v := *numerator / *denominator
	return &v
}

// asRatio converts a value that may be a DefiLlama percentage (e.g. 30 for
// 30%) into a ratio (0.30): DefiLlama reports some margins as percentages,
// FD expects ratios. Ported verbatim from service._as_ratio: values whose
// absolute magnitude exceeds 1.5 are assumed to be percentages.
func asRatio(value *float64) *float64 {
	if value == nil {
		return nil
	}
	v := *value
	if math.Abs(v) > 1.5 {
		v /= 100
	}
	return &v
}

func clonePtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	return &c
}

// BuildFinancialMetricSnapshot builds the FD FinancialMetricSnapshot for
// one ticker, mirroring service.get_financial_metrics_snapshot +
// fd.metric_snapshot_record. gross_margin and net_margin are derived
// locally as gross_profit_ttm/revenue_ttm and net_income_ttm/revenue_ttm;
// operating_margin is DefiLlama's reported margin normalized to a ratio.
func BuildFinancialMetricSnapshot(ticker string, s Summary) fd.FinancialMetricSnapshot {
	symbol := ticker
	return fd.FinancialMetricSnapshot{
		Ticker:                       &symbol,
		MarketCap:                    clonePtr(s.MarketCap),
		EnterpriseValue:              clonePtr(s.EnterpriseValue),
		PriceToEarningsRatio:         clonePtr(s.PriceToEarnings),
		PriceToBookRatio:             clonePtr(s.PriceToBook),
		PriceToSalesRatio:            clonePtr(s.PriceToRevenue),
		EnterpriseValueToEBITDARatio: clonePtr(s.EnterpriseValueToEBITDA),
		GrossMargin:                  ratio(s.GrossProfitTTM, s.RevenueTTM),
		OperatingMargin:              asRatio(s.OperatingProfitMarginTTM),
		NetMargin:                    ratio(s.NetIncomeTTM, s.RevenueTTM),
	}
}
