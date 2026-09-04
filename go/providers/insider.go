// This file ports insider_trades.py (SECForm4 /search response parsing,
// strict shape validation, and local filters) plus
// fd.insider_trade_record: turning a SECForm4 /search payload into FD
// InsiderTrade records.
package providers

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
)

// insiderMaxRows is the validated ceiling on SECForm4 /search results.
const insiderMaxRows = 15

var (
	insiderTransactionRe = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2}) ([A-Za-z][A-Za-z /-]*)$`)
	insiderOwnershipRe   = regexp.MustCompile(`^(\S+) \(([^()]+)\)$`)
	insiderNumberRe      = regexp.MustCompile(`^-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$`)
	insiderMoneyRe       = regexp.MustCompile(`^\$(-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?)$`)
)

func insiderParseNumber(raw string) (float64, bool) {
	if !insiderNumberRe.MatchString(raw) {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func insiderParseMoney(raw string) (float64, bool) {
	m := insiderMoneyRe.FindStringSubmatch(raw)
	if m == nil {
		return 0, false
	}
	return insiderParseNumber(m[1])
}

// insiderReportedDatetimeLayouts are the strptime "%Y-%m-%d %I:%M %p"
// equivalents Go needs to accept both zero-padded and single-digit hours
// (e.g. "2026-09-03 6:30 pm").
var insiderReportedDatetimeLayouts = []string{"2006-01-02 3:04 pm", "2006-01-02 03:04 pm"}

func insiderParseReportedDatetime(raw string) (time.Time, bool) {
	for _, layout := range insiderReportedDatetimeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// insiderRow is one normalized SECForm4 result row, before FD shaping and
// filtering.
type insiderRow struct {
	company             string
	transactionDate     string // YYYY-MM-DD
	reported            time.Time
	filingDate          string // YYYY-MM-DD, reported's date component
	insiderRelationship string
	transactionType     string
	sharesTraded        float64
	averagePrice        float64
	totalAmount         float64
	sharesOwned         float64
}

// NormalizeInsiderTrades parses a SECForm4 /search payload into FD
// InsiderTrade records for one ticker, mirroring
// insider_trades.normalize_insider_trades + fd.insider_trade_record.
// Results are sorted newest-reported-first and truncated to limit.
//
// name, transactionType, filingDate, filingDateGTE, filingDateLTE are
// optional local filters (name is a case-insensitive substring match
// against insider_relationship; transactionType is a case-insensitive
// exact match against the parsed action; the filing_date filters compare
// against reported's date component).
func NormalizeInsiderTrades(raw json.RawMessage, ticker string, limit int, name, transactionType, filingDate, filingDateGTE, filingDateLTE *string) ([]fd.InsiderTrade, error) {
	value, err := unmarshalAny(raw)
	if err != nil {
		return nil, err
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, schemaDriftf("SECForm4 search payload must be an object")
	}
	if status, _ := root["status"].(string); status != "success" {
		return nil, schemaDriftf("SECForm4 search status must be 'success'")
	}
	data, ok := root["data"].(map[string]any)
	if !ok {
		return nil, schemaDriftf("SECForm4 search data must be an object")
	}
	query, ok := data["query"].(string)
	if !ok || query == "" {
		return nil, schemaDriftf("SECForm4 search query must be a non-empty string")
	}
	if strings.ToUpper(query) != ticker {
		return nil, schemaDriftf("SECForm4 search query does not match the requested ticker")
	}
	resultsValue, ok := data["results"].([]any)
	if !ok {
		return nil, schemaDriftf("SECForm4 search results must be an array")
	}
	if len(resultsValue) > insiderMaxRows {
		return nil, schemaDriftf("SECForm4 search returned more than its validated %d-row ceiling", insiderMaxRows)
	}

	rows := make([]insiderRow, 0, len(resultsValue))
	for index, item := range resultsValue {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d] must be an object", index)
		}
		symbol, ok := row["symbol"].(string)
		if !ok || symbol == "" {
			return nil, schemaDriftf("SECForm4 result[%d].symbol must be a non-empty string", index)
		}
		if strings.ToUpper(symbol) != ticker {
			return nil, schemaDriftf("SECForm4 result[%d] symbol does not match %s", index, ticker)
		}
		transactionRaw, ok := row["transaction_date"].(string)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].transaction_date must be a non-empty string", index)
		}
		txMatch := insiderTransactionRe.FindStringSubmatch(transactionRaw)
		if txMatch == nil {
			return nil, schemaDriftf("SECForm4 result[%d].transaction_date must contain an ISO date and one action", index)
		}
		reportedRaw, ok := row["reported_datetime"].(string)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].reported_datetime must be a non-empty string", index)
		}
		reported, ok := insiderParseReportedDatetime(reportedRaw)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].reported_datetime must use YYYY-MM-DD h:mm am/pm", index)
		}
		relationship, ok := row["insider_relationship"].(string)
		if !ok || relationship == "" {
			return nil, schemaDriftf("SECForm4 result[%d].insider_relationship must be a non-empty string", index)
		}
		company, ok := row["company"].(string)
		if !ok || company == "" {
			return nil, schemaDriftf("SECForm4 result[%d].company must be a non-empty string", index)
		}
		sharesTradedRaw, ok := row["shares_traded"].(string)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].shares_traded must be a non-empty string", index)
		}
		sharesTraded, ok := insiderParseNumber(sharesTradedRaw)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].shares_traded must be unambiguous numeric data", index)
		}
		averagePriceRaw, ok := row["average_price"].(string)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].average_price must be a non-empty string", index)
		}
		averagePrice, ok := insiderParseMoney(averagePriceRaw)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].average_price must be an unambiguous dollar amount", index)
		}
		totalAmountRaw, ok := row["total_amount"].(string)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].total_amount must be a non-empty string", index)
		}
		totalAmount, ok := insiderParseMoney(totalAmountRaw)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].total_amount must be an unambiguous dollar amount", index)
		}
		sharesOwnedRaw, ok := row["shares_owned"].(string)
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].shares_owned must be a non-empty string", index)
		}
		ownMatch := insiderOwnershipRe.FindStringSubmatch(sharesOwnedRaw)
		if ownMatch == nil {
			return nil, schemaDriftf("SECForm4 result[%d].shares_owned must contain shares followed by ownership text", index)
		}
		sharesOwned, ok := insiderParseNumber(ownMatch[1])
		if !ok {
			return nil, schemaDriftf("SECForm4 result[%d].shares_owned must be unambiguous numeric data", index)
		}

		rows = append(rows, insiderRow{
			company:             company,
			transactionDate:     txMatch[1],
			reported:            reported,
			filingDate:          reported.Format("2006-01-02"),
			insiderRelationship: relationship,
			transactionType:     txMatch[2],
			sharesTraded:        sharesTraded,
			averagePrice:        averagePrice,
			totalAmount:         totalAmount,
			sharesOwned:         sharesOwned,
		})
	}

	filtered := make([]insiderRow, 0, len(rows))
	for _, row := range rows {
		if name != nil && !strings.Contains(strings.ToLower(row.insiderRelationship), strings.ToLower(*name)) {
			continue
		}
		if transactionType != nil && !strings.EqualFold(row.transactionType, *transactionType) {
			continue
		}
		if filingDate != nil && row.filingDate != *filingDate {
			continue
		}
		if filingDateGTE != nil && row.filingDate < *filingDateGTE {
			continue
		}
		if filingDateLTE != nil && row.filingDate > *filingDateLTE {
			continue
		}
		filtered = append(filtered, row)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].reported.After(filtered[j].reported) })
	if limit >= 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}

	trades := make([]fd.InsiderTrade, 0, len(filtered))
	for _, row := range filtered {
		symbol := ticker
		issuer := row.company
		insiderName := row.insiderRelationship
		filingDateVal := row.filingDate
		transactionDate := row.transactionDate
		transactionType := row.transactionType
		shares := row.sharesTraded
		pricePerShare := row.averagePrice
		value := row.totalAmount
		owned := row.sharesOwned
		trades = append(trades, fd.InsiderTrade{
			Ticker:                      &symbol,
			Issuer:                      &issuer,
			Name:                        &insiderName,
			FilingDate:                  &filingDateVal,
			TransactionDate:             &transactionDate,
			TransactionType:             &transactionType,
			TransactionShares:           &shares,
			TransactionPricePerShare:    &pricePerShare,
			TransactionValue:            &value,
			SharesOwnedAfterTransaction: &owned,
		})
	}
	return trades, nil
}
