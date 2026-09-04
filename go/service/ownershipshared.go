// This file holds parsing and lookup helpers shared by the four
// ownership-state tools this port newly implements
// (get_beneficial_owners, get_beneficial_ownership, get_insider_ownership,
// get_institutional_investors): the SECForm4 13D/13G feed fetch, its
// compound-string field parsers, and the ticker->CIK derivation every
// CIK-keyed SECForm4 endpoint needs.
//
// Freshness: the SECForm4 13D/13G feed this file reads from was measured
// live to be roughly six months behind the current date (see
// docs/compatibility.md). Every record built from it carries its own
// filing/transaction date so a caller can judge recency; nothing here
// ever describes this data as current or real-time.
package service

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/providers"
)

const (
	beneficial13DEndpoint  = "/get_13d_filings"
	beneficial13GEndpoint  = "/get_13g_filings"
	insiderTradingEndpoint = "/get_company_insider_trading"
	hedgeFundEndpoint      = "/get_hedge_fund_portfolio"
)

// statusEnvelopeRows extracts the row list from a SECForm4 list-endpoint
// payload shaped {"status":"success","data":{"items":[...]}} (the
// /get_13d_filings and /get_13g_filings envelope verified live), tolerating
// a bare list under "data" or a couple of plausible alternate keys the
// same way institutionalholdings.go's row-key list does, since this port
// has not independently verified every SECForm4 endpoint has the exact
// same envelope.
var statusEnvelopeRowKeys = []string{"items", "results", "rows", "data"}

func statusEnvelopeRows(raw json.RawMessage, endpointName string) ([]map[string]any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 " + endpointName + " payload must be valid JSON"}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 " + endpointName + " payload must be an object"}
	}
	if status, _ := root["status"].(string); status != "success" {
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 " + endpointName + " status must be 'success'"}
	}
	data, ok := root["data"]
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 " + endpointName + " omitted a data object"}
	}
	switch typed := data.(type) {
	case []any:
		return statusEnvelopeObjectRows(typed, endpointName)
	case map[string]any:
		for _, key := range statusEnvelopeRowKeys {
			if child, ok := typed[key].([]any); ok {
				return statusEnvelopeObjectRows(child, endpointName)
			}
		}
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 " + endpointName + " omitted its row list"}
	default:
		return nil, &providers.SchemaDriftError{Msg: "SECForm4 " + endpointName + " data must be an object or array"}
	}
}

func statusEnvelopeObjectRows(values []any, endpointName string) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(values))
	for _, item := range values {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, &providers.SchemaDriftError{Msg: "SECForm4 " + endpointName + " row must be an object"}
		}
		rows = append(rows, obj)
	}
	return rows, nil
}

// --- compound-string field parsers ---
//
// SECForm4's 13D/13G feed packs two values into one string field rather
// than returning them separately (verified live): shares_owned_owned is
// "<shares> <percent>%" (e.g. "1,171,112 16.070%") and
// shares_vs_prev_report is "<signed delta> (<signed percent>%)" (e.g.
// "204,765 (+21.19%)"). Each parser below extracts whichever half of the
// pair is unambiguously parseable and leaves the other nil rather than
// guessing; a completely unrecognized string leaves both nil.

var (
	sharesAndPercentRe = regexp.MustCompile(`^([0-9][0-9,]*(?:\.[0-9]+)?)\s+([0-9]+(?:\.[0-9]+)?)%$`)
	leadingSharesRe    = regexp.MustCompile(`^([0-9][0-9,]*(?:\.[0-9]+)?)\b`)
	trailingPercentRe  = regexp.MustCompile(`([+-]?[0-9]+(?:\.[0-9]+)?)%\s*$`)
	deltaAndPercentRe  = regexp.MustCompile(`^([+-]?[0-9][0-9,]*(?:\.[0-9]+)?)\s+\(([+-]?[0-9]+(?:\.[0-9]+)?)%\)$`)
	leadingDeltaRe     = regexp.MustCompile(`^([+-]?[0-9][0-9,]*(?:\.[0-9]+)?)\b`)
)

func parseCommaFloat(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", ""), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseSharesAndPercent splits SECForm4's shares_owned_owned field into an
// absolute share count and a percent-of-class figure, omitting whichever
// half does not unambiguously parse.
func parseSharesAndPercent(raw string) (shares *float64, percent *float64) {
	trimmed := strings.TrimSpace(raw)
	if m := sharesAndPercentRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			shares = &v
		}
		if v, ok := parseCommaFloat(m[2]); ok {
			percent = &v
		}
		return shares, percent
	}
	if m := leadingSharesRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			shares = &v
		}
	}
	if m := trailingPercentRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			percent = &v
		}
	}
	return shares, percent
}

// parseDeltaAndPercent splits SECForm4's shares_vs_prev_report field into a
// signed share-count delta and a signed percent-change figure, omitting
// whichever half does not unambiguously parse.
func parseDeltaAndPercent(raw string) (delta *float64, percent *float64) {
	trimmed := strings.TrimSpace(raw)
	if m := deltaAndPercentRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			delta = &v
		}
		if v, ok := parseCommaFloat(m[2]); ok {
			percent = &v
		}
		return delta, percent
	}
	if m := leadingDeltaRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			delta = &v
		}
	}
	if m := trailingPercentRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			percent = &v
		}
	}
	return delta, percent
}

// secform4DateOnly reads the first 10 characters of a SECForm4 date-ish
// field (a plain "YYYY-MM-DD" or a "YYYY-MM-DD h:mm am/pm" reported
// timestamp) and reports whether they parse as an ISO calendar date.
func secform4DateOnly(raw string) (string, bool) {
	text := raw
	if len(text) > 10 {
		text = text[:10]
	}
	if _, err := time.Parse(dateLayout, text); err != nil {
		return "", false
	}
	return text, true
}

// --- ticker -> CIK derivation ---
//
// get_company_insider_trading and get_hedge_fund_portfolio take a SEC CIK,
// not a ticker. This server derives a ticker's CIK from the same filings
// lookup get_filings already uses (defillama's /equities/v1/filings,
// reused verbatim so callCtx.run's cache applies), by reading the CIK
// segment out of the returned filing URL's
// ".../Archives/edgar/data/<cik>/..." path, mirroring the format the
// caller's own get_filings responses already expose. Never guessed: a
// ticker with no filing carrying a well-formed CIK segment resolves to
// found=false, which callers render as their tool's normal not_found
// result rather than a fabricated identifier.
var edgarCIKPattern = regexp.MustCompile(`(?i)/archives/edgar/data/([0-9]{1,10})(?:/|$)`)

// cikFromEdgarURL extracts and validates the CIK segment from one SEC
// EDGAR archive URL, or returns nil when the URL has none.
func cikFromEdgarURL(rawURL string) *string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	m := edgarCIKPattern.FindStringSubmatch(strings.ToLower(parsed.Path))
	if m == nil {
		return nil
	}
	cik := strings.TrimLeft(m[1], "0")
	if cik == "" {
		return nil // "0000000000" is not a real CIK
	}
	return &cik
}

// resolveIssuerCIK derives ticker's SEC CIK from its filings, reusing the
// exact provider/endpoint/query-params get_filings issues so this shares
// that call's cache entry, per this port's brief. found is false (with a
// nil error) when the filings lookup succeeded but no returned filing URL
// carried a well-formed CIK segment; the caller renders that as its own
// tool's not_found result.
func (c *callCtx) resolveIssuerCIK(ticker string) (cik string, found bool, err error) {
	run, err := c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": ticker, "country": "US"})
	if err != nil {
		return "", false, err
	}
	var value any
	if err := json.Unmarshal(run.Output, &value); err != nil {
		return "", false, &providers.SchemaDriftError{Msg: "provider payload is not valid JSON"}
	}
	records, err := extractGenericRecords(value, []string{"filings", "results", "data"})
	if err != nil {
		return "", false, err
	}
	for _, record := range records {
		urlValue := firstStringGeneric(record, "primary_document_url", "primaryDocumentUrl", "url")
		if urlValue == nil {
			continue
		}
		if cikPtr := cikFromEdgarURL(*urlValue); cikPtr != nil {
			return *cikPtr, true, nil
		}
	}
	return "", false, nil
}

// --- shares_owned "<shares> (<nature>)" compound parsing ---
//
// Some SECForm4 insider routes pack an ownership-nature label alongside
// the share count (e.g. "12,345 (Direct)"), the same "value (label)"
// shape providers/insider.go's own shares_owned field already uses for
// /search. parseSharesAndOwnership accepts both that compound form and a
// bare numeric string, omitting whichever half does not unambiguously
// parse.
var sharesAndOwnershipRe = regexp.MustCompile(`^([0-9][0-9,]*(?:\.[0-9]+)?)\s*\(([A-Za-z][A-Za-z ]*)\)$`)

func parseSharesAndOwnership(raw string) (shares *float64, nature *string) {
	trimmed := strings.TrimSpace(raw)
	if m := sharesAndOwnershipRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			shares = &v
		}
		label := strings.TrimSpace(m[2])
		if label != "" {
			nature = &label
		}
		return shares, nature
	}
	if m := leadingSharesRe.FindStringSubmatch(trimmed); m != nil {
		if v, ok := parseCommaFloat(m[1]); ok {
			shares = &v
		}
	}
	return shares, nature
}

// --- local accession-number derivation ---
//
// deriveAccessionLocal duplicates providers.DeriveAccession's small,
// self-contained digit-grouping search (go/providers/sec.go) for use from
// go/service without adding a go/providers dependency for one field this
// port cannot otherwise verify the source shape of; see
// insiderownership.go's doc comment.
func deriveAccessionLocal(rawURL string) *string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return findAccessionLocal(parsed.Path)
}

func findAccessionLocal(path string) *string {
	n := len(path)
	for i := 0; i < n; i++ {
		if !isASCIIDigitLocal(path[i]) {
			continue
		}
		if i > 0 && isASCIIDigitLocal(path[i-1]) {
			continue
		}
		pos := i
		group1, ok := takeDigitsLocal(path, &pos, 10)
		if !ok {
			continue
		}
		skipDashLocal(path, &pos)
		group2, ok := takeDigitsLocal(path, &pos, 2)
		if !ok {
			continue
		}
		skipDashLocal(path, &pos)
		group3, ok := takeDigitsLocal(path, &pos, 6)
		if !ok {
			continue
		}
		if pos < n && isASCIIDigitLocal(path[pos]) {
			continue
		}
		accession := group1 + "-" + group2 + "-" + group3
		return &accession
	}
	return nil
}

func isASCIIDigitLocal(b byte) bool { return b >= '0' && b <= '9' }

func takeDigitsLocal(s string, pos *int, count int) (string, bool) {
	if *pos+count > len(s) {
		return "", false
	}
	for k := *pos; k < *pos+count; k++ {
		if !isASCIIDigitLocal(s[k]) {
			return "", false
		}
	}
	out := s[*pos : *pos+count]
	*pos += count
	return out, true
}

func skipDashLocal(s string, pos *int) {
	if *pos < len(s) && s[*pos] == '-' {
		*pos++
	}
}

// stripLeadingDollar removes one leading "$" from a money-ish string (and
// trims surrounding whitespace), used by both the beneficial-ownership
// compound parsers' siblings in ipocalendar.go/marketsnapshot.go and any
// future money-string field this port sources as "$1,234.56".
func stripLeadingDollar(raw string) string {
	trimmed := strings.TrimSpace(raw)
	return strings.TrimPrefix(trimmed, "$")
}
