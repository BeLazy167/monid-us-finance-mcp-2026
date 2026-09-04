// This file ports stock_screener.py (request validation + Nasdaq
// /get_stock_screener response parsing) plus service._metric_string and
// fd.screener_search_result/screener_filters_response: turning a validated
// screen_stocks filter list into a Nasdaq query, and a Nasdaq screener
// payload into FD search_results rows with string metric values.
package providers

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var screenerAllowedExchanges = map[string]bool{"NASDAQ": true, "NYSE": true, "AMEX": true}
var screenerAllowedMarketCaps = map[string]bool{
	"mega": true, "large": true, "mid": true, "small": true, "micro": true, "nano": true,
}

var (
	screenerNumberRe  = regexp.MustCompile(`^-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$`)
	screenerMoneyRe   = regexp.MustCompile(`^\$(-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?)$`)
	screenerPercentRe = regexp.MustCompile(`^(-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?)%$`)
	screenerAsOfRe    = regexp.MustCompile(`^Last price as of ([A-Z][a-z]{2} [0-9]{1,2}, [0-9]{4})$`)
)

// ScreenerRequest is a validated screen_stocks request, ready to become
// Nasdaq /get_stock_screener query parameters.
type ScreenerRequest struct {
	QueryParams map[string]string
	Limit       int
	Offset      int
}

// ValidateScreenerRequest validates a screen_stocks filters list against
// the two supported fields (exchange, market_cap), the eq operator, and
// their static value catalogs, mirroring
// stock_screener.validate_screener_request. filters use the same loose
// map[string]any shape the MCP tool call receives, so malformed filter
// objects (wrong key set, non-string values, ...) are rejected exactly as
// the Python route rejects them.
func ValidateScreenerRequest(filters []map[string]any, limit, offset int) (ScreenerRequest, error) {
	if len(filters) == 0 || len(filters) > 2 {
		return ScreenerRequest{}, newInputErrorf("filters must contain one or two supported filters")
	}
	if limit < 1 || limit > 100 {
		return ScreenerRequest{}, newInputErrorf("limit must be between 1 and 100")
	}
	if offset < 0 {
		return ScreenerRequest{}, newInputErrorf("offset must be a non-negative integer")
	}
	query := map[string]string{}
	seen := map[string]bool{}
	for index, item := range filters {
		if !screenerHasExactKeys(item, "field", "operator", "value") {
			return ScreenerRequest{}, newInputErrorf("filters[%d] must contain only field, operator, and value", index)
		}
		field, _ := item["field"].(string)
		if field != "exchange" && field != "market_cap" {
			return ScreenerRequest{}, newInputErrorf("only exchange and market_cap filters are supported")
		}
		if seen[field] {
			return ScreenerRequest{}, newInputErrorf("filter field %s may appear only once", field)
		}
		seen[field] = true
		operator, _ := item["operator"].(string)
		if operator != "eq" {
			return ScreenerRequest{}, newInputErrorf("only the eq stock-screener operator is supported")
		}
		value, ok := item["value"].(string)
		if !ok {
			return ScreenerRequest{}, newInputErrorf("%s filter value must be a string", field)
		}
		if field == "exchange" {
			if !screenerAllowedExchanges[value] {
				return ScreenerRequest{}, newInputErrorf("exchange must be NASDAQ, NYSE, or AMEX")
			}
		} else if !screenerAllowedMarketCaps[value] {
			return ScreenerRequest{}, newInputErrorf("market_cap must be mega, large, mid, small, micro, or nano")
		}
		query[field] = value
	}
	return ScreenerRequest{QueryParams: query, Limit: limit, Offset: offset}, nil
}

func screenerHasExactKeys(item map[string]any, keys ...string) bool {
	if len(item) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := item[key]; !ok {
			return false
		}
	}
	return true
}

// screenerNumber is a parsed Nasdaq screener metric, keeping enough of the
// source text's shape (had-a-decimal-point or not) to render back to a
// string the way Python's int/float distinction would.
type screenerNumber struct {
	value    float64
	wasFloat bool
}

func screenerParseNumber(raw string) (screenerNumber, bool) {
	if !screenerNumberRe.MatchString(raw) {
		return screenerNumber{}, false
	}
	compact := strings.ReplaceAll(raw, ",", "")
	v, err := strconv.ParseFloat(compact, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return screenerNumber{}, false
	}
	return screenerNumber{value: v, wasFloat: strings.Contains(compact, ".")}, true
}

func screenerParseMoney(raw string) (screenerNumber, bool) {
	m := screenerMoneyRe.FindStringSubmatch(raw)
	if m == nil {
		return screenerNumber{}, false
	}
	return screenerParseNumber(m[1])
}

// screenerParsePercent parses "-1.00%" into the ratio -0.01, mirroring
// stock_screener._percent (always a Python float after the /100 division).
func screenerParsePercent(raw string) (screenerNumber, bool) {
	m := screenerPercentRe.FindStringSubmatch(raw)
	if m == nil {
		return screenerNumber{}, false
	}
	n, ok := screenerParseNumber(m[1])
	if !ok {
		return screenerNumber{}, false
	}
	return screenerNumber{value: n.value / 100, wasFloat: true}, true
}

// metricString renders a screener metric value the way service._metric_string
// renders a Python int/float: an integral value (or one that was already
// a Python int) prints with no decimal point; otherwise it prints rounded
// to 6 decimal places.
func metricString(n screenerNumber) string {
	if !n.wasFloat || n.value == math.Trunc(n.value) {
		return strconv.FormatInt(int64(math.Round(n.value)), 10)
	}
	rounded := math.Round(n.value*1e6) / 1e6
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// ScreenerRow is one parsed Nasdaq stock-screener table row.
type ScreenerRow struct {
	Ticker        string
	Name          string
	LastSale      screenerNumber
	NetChange     screenerNumber
	PercentChange screenerNumber
	MarketCap     screenerNumber
	SourcePath    string
}

var screenerExpectedHeaders = map[string]bool{
	"symbol": true, "name": true, "lastsale": true, "netchange": true, "pctchange": true, "marketCap": true,
}

// NormalizeScreener parses a Nasdaq /get_stock_screener payload into rows,
// mirroring stock_screener.normalize_screener's strict shape checks
// (status, headers, provider status code, totalrecords, asof format).
func NormalizeScreener(raw json.RawMessage) ([]ScreenerRow, error) {
	value, err := unmarshalAny(raw)
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, schemaDriftf("Nasdaq stock-screener payload must be an object")
	}
	if status, _ := root["status"].(string); status != "success" {
		return nil, schemaDriftf("Nasdaq stock-screener status must be 'success'")
	}
	outer, ok := root["data"].(map[string]any)
	if !ok {
		return nil, schemaDriftf("Nasdaq stock-screener data must be an object")
	}
	data, ok := outer["data"].(map[string]any)
	if !ok {
		return nil, schemaDriftf("Nasdaq stock-screener nested data must be an object")
	}
	if _, ok := data["filters"].(map[string]any); !ok {
		return nil, schemaDriftf("Nasdaq stock-screener filters must be an object")
	}
	table, ok := data["table"].(map[string]any)
	if !ok {
		return nil, schemaDriftf("Nasdaq stock-screener table must be an object")
	}
	headers, ok := table["headers"].(map[string]any)
	if !ok {
		return nil, schemaDriftf("Nasdaq stock-screener headers must be an object")
	}
	if len(headers) != len(screenerExpectedHeaders) {
		return nil, schemaDriftf("Nasdaq stock-screener headers changed")
	}
	for key, value := range headers {
		if !screenerExpectedHeaders[key] {
			return nil, schemaDriftf("Nasdaq stock-screener headers changed")
		}
		if _, ok := value.(string); !ok {
			return nil, schemaDriftf("Nasdaq stock-screener headers changed")
		}
	}
	rowsValue, ok := table["rows"].([]any)
	if !ok {
		return nil, schemaDriftf("Nasdaq stock-screener rows must be an array")
	}
	totalRecords, ok := numberValue(data["totalrecords"])
	if !ok || totalRecords < 0 || totalRecords != math.Trunc(totalRecords) {
		return nil, schemaDriftf("Nasdaq stock-screener totalrecords must be non-negative")
	}
	asOfRaw, ok := data["asof"].(string)
	if !ok || asOfRaw == "" {
		return nil, schemaDriftf("Nasdaq stock-screener asof must be a non-empty string")
	}
	if _, err := screenerParseAsOf(asOfRaw); err != nil {
		return nil, err
	}
	providerStatus, ok := outer["status"].(map[string]any)
	if !ok {
		return nil, schemaDriftf("Nasdaq stock-screener response status must be an object")
	}
	rCode, ok := numberValue(providerStatus["rCode"])
	if !ok || rCode != 200 {
		return nil, schemaDriftf("Nasdaq stock-screener response status is not 200")
	}

	rows := make([]ScreenerRow, 0, len(rowsValue))
	for index, item := range rowsValue {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, schemaDriftf("Nasdaq stock-screener row[%d] must be an object", index)
		}
		symbol, ok := row["symbol"].(string)
		if !ok || symbol == "" {
			return nil, schemaDriftf("Nasdaq row[%d].symbol must be a non-empty string", index)
		}
		name, ok := row["name"].(string)
		if !ok || name == "" {
			return nil, schemaDriftf("Nasdaq row[%d].name must be a non-empty string", index)
		}
		lastSaleRaw, ok := row["lastsale"].(string)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].lastsale must be a non-empty string", index)
		}
		lastSale, ok := screenerParseMoney(lastSaleRaw)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].lastsale must be an unambiguous dollar amount", index)
		}
		netChangeRaw, ok := row["netchange"].(string)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].netchange must be a non-empty string", index)
		}
		netChange, ok := screenerParseNumber(netChangeRaw)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].netchange must be unambiguous numeric data", index)
		}
		pctChangeRaw, ok := row["pctchange"].(string)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].pctchange must be a non-empty string", index)
		}
		percentChange, ok := screenerParsePercent(pctChangeRaw)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].pctchange must be an unambiguous percentage", index)
		}
		marketCapRaw, ok := row["marketCap"].(string)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].marketCap must be a non-empty string", index)
		}
		marketCap, ok := screenerParseNumber(marketCapRaw)
		if !ok {
			return nil, schemaDriftf("Nasdaq row[%d].marketCap must be unambiguous numeric data", index)
		}
		sourcePath, _ := row["url"].(string)
		rows = append(rows, ScreenerRow{
			Ticker: strings.ToUpper(symbol), Name: name,
			LastSale: lastSale, NetChange: netChange, PercentChange: percentChange, MarketCap: marketCap,
			SourcePath: sourcePath,
		})
	}
	return rows, nil
}

func screenerParseAsOf(raw string) (string, error) {
	m := screenerAsOfRe.FindStringSubmatch(raw)
	if m == nil {
		return "", schemaDriftf("Nasdaq stock-screener asof changed format")
	}
	parsed, err := time.Parse("Jan 2, 2006", m[1])
	if err != nil {
		return "", schemaDriftf("Nasdaq stock-screener asof contains an invalid date")
	}
	return parsed.Format("2006-01-02"), nil
}

// SearchResult is one Financial Datasets search_results row: every metric
// is rendered as a string (percent values already converted to ratios),
// mirroring fd.screener_search_result + service._metric_string. There is
// no fixed Financial Datasets schema for this row (item_ref is null in the
// contract), so this type lives in the providers package rather than fd.
type SearchResult struct {
	Ticker        string  `json:"ticker"`
	Exchange      *string `json:"exchange,omitempty"`
	MarketCap     *string `json:"market_cap,omitempty"`
	LastSale      *string `json:"last_sale,omitempty"`
	NetChange     *string `json:"net_change,omitempty"`
	PercentChange *string `json:"percent_change,omitempty"`
}

// BuildSearchResults renders screener rows into FD search_results, capped
// at limit and stamped with the single requested exchange (if any),
// mirroring service.screen_stocks's result-building loop.
func BuildSearchResults(rows []ScreenerRow, exchange *string, limit int) []SearchResult {
	if limit >= 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	results := make([]SearchResult, 0, len(rows))
	for _, row := range rows {
		marketCap := metricString(row.MarketCap)
		lastSale := metricString(row.LastSale)
		netChange := metricString(row.NetChange)
		percentChange := metricString(row.PercentChange)
		result := SearchResult{
			Ticker:        row.Ticker,
			MarketCap:     &marketCap,
			LastSale:      &lastSale,
			NetChange:     &netChange,
			PercentChange: &percentChange,
		}
		if exchange != nil {
			ex := *exchange
			result.Exchange = &ex
		}
		results = append(results, result)
	}
	return results
}

// ScreenerFilterField mirrors one entry of the Financial Datasets
// list_stock_screener_filters catalog.
type ScreenerFilterField struct {
	Field     string   `json:"field"`
	Operators []string `json:"operators"`
	Values    []string `json:"values"`
}

// ScreenerFiltersCatalog is the executable filter catalog for the
// validated Nasdaq screener route, mirroring fd.screener_filters_response.
type ScreenerFiltersCatalog struct {
	Metrics   map[string][]ScreenerFilterField `json:"metrics"`
	Operators []string                         `json:"operators"`
}

// ScreenerFilters returns the static filter catalog for screen_stocks,
// mirroring fd.screener_filters_response.
func ScreenerFilters() ScreenerFiltersCatalog {
	exchanges := make([]string, 0, len(screenerAllowedExchanges))
	for k := range screenerAllowedExchanges {
		exchanges = append(exchanges, k)
	}
	sort.Strings(exchanges)
	caps := []string{"mega", "large", "mid", "small", "micro", "nano"}
	return ScreenerFiltersCatalog{
		Metrics: map[string][]ScreenerFilterField{
			"company": {
				{Field: "exchange", Operators: []string{"eq"}, Values: exchanges},
				{Field: "market_cap", Operators: []string{"eq"}, Values: caps},
			},
		},
		Operators: []string{"eq"},
	}
}
