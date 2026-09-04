// This file ports index_fund.py: discovering and parsing index/ETF fund
// holdings from public fact sheets via Context.dev /web/search +
// /web/scrape/markdown. XLSX-only holdings documents are not routable (the
// Monid artifact fetcher downloads JSON only), so this port answers with
// an honest error when nothing parseable comes back. Only values stated on
// the page are emitted; unsourced fields are omitted.
package service

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

// knownIssuerDomains mirrors index_fund.KNOWN_ISSUER_DOMAINS.
var knownIssuerDomains = map[string][]string{
	"SPY": {"ssga.com"},
	"IVV": {"ssga.com"},
	"QQQ": {"invesco.com"},
}

var blockedHoldingsExtensions = []string{".xlsx", ".xls", ".csv"}

// indexFundSearchRequestBody mirrors index_fund.index_fund_search_request(ticker, include_domains=True).
func indexFundSearchRequestBody(ticker string) map[string]any {
	body := map[string]any{
		"query":           ticker + " ETF holdings xlsx",
		"numResults":      10,
		"markdownOptions": map[string]any{"enabled": false},
		"timeoutMS":       60_000,
	}
	if domains, ok := knownIssuerDomains[ticker]; ok && len(domains) > 0 {
		list := make([]any, len(domains))
		for i, d := range domains {
			list[i] = d
		}
		body["includeDomains"] = list
	}
	return body
}

type indexFundCandidate struct {
	URL   string
	Title *string
}

// searchResultRows mirrors index_fund.search_rows: descends up to 5 levels
// through the /web/search wrapper keys.
func searchResultRows(value any) ([]map[string]any, error) {
	current := value
	for i := 0; i < 5; i++ {
		if arr, ok := current.([]any); ok {
			rows := make([]map[string]any, 0, len(arr))
			for index, item := range arr {
				obj, ok := item.(map[string]any)
				if !ok {
					return nil, &providers.SchemaDriftError{Msg: "Web search result[" + strconv.Itoa(index) + "] must be an object"}
				}
				rows = append(rows, obj)
			}
			return rows, nil
		}
		obj, ok := current.(map[string]any)
		if !ok {
			break
		}
		var child any
		for _, key := range []string{"results", "web_results", "search_results", "data", "items"} {
			if c, exists := obj[key]; exists {
				switch c.(type) {
				case []any, map[string]any:
					child = c
				}
			}
			if child != nil {
				break
			}
		}
		if child == nil {
			break
		}
		current = child
	}
	return nil, &providers.SchemaDriftError{Msg: "Web search payload omitted the result list"}
}

// pickHoldingsCandidates mirrors index_fund.pick_holdings_candidates:
// scores each result row (issuer-domain host match +3, non-blocked
// extension +1, title containing "holding" +1), keeps only https:// URLs,
// and orders candidates by (score desc, original index asc).
func pickHoldingsCandidates(raw json.RawMessage, ticker string) ([]indexFundCandidate, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "Web search payload is not valid JSON"}
	}
	rows, err := searchResultRows(value)
	if err != nil {
		return nil, err
	}
	preferred := knownIssuerDomains[ticker]

	type scored struct {
		score int
		index int
		cand  indexFundCandidate
	}
	var candidates []scored
	for index, row := range rows {
		rawURL, ok := row["url"].(string)
		if !ok || rawURL == "" {
			if link, ok2 := row["link"].(string); ok2 {
				rawURL = link
			}
		}
		if rawURL == "" || !strings.HasPrefix(rawURL, "https://") {
			continue
		}
		score := indexFundCandidateScore(row, rawURL, preferred)
		var title *string
		if t, ok := row["title"].(string); ok && t != "" {
			title = &t
		}
		candidates = append(candidates, scored{score: score, index: index, cand: indexFundCandidate{URL: rawURL, Title: title}})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].index < candidates[j].index
	})
	out := make([]indexFundCandidate, len(candidates))
	for i, c := range candidates {
		out[i] = c.cand
	}
	return out, nil
}

func indexFundCandidateScore(row map[string]any, rawURL string, preferred []string) int {
	total := 0
	parsed, err := url.Parse(rawURL)
	if err == nil {
		host := strings.ToLower(parsed.Hostname())
		for _, domain := range preferred {
			if strings.HasSuffix(host, domain) {
				total += 3
				break
			}
		}
		path := strings.ToLower(parsed.Path)
		blocked := false
		for _, ext := range blockedHoldingsExtensions {
			if strings.HasSuffix(path, ext) {
				blocked = true
				break
			}
		}
		if !blocked {
			total++
		}
	}
	if title, ok := row["title"].(string); ok && strings.Contains(strings.ToLower(title), "holding") {
		total++
	}
	return total
}

// indexFundScrapeQuery mirrors index_fund.scrape_query.
func indexFundScrapeQuery(url string) map[string]any {
	return map[string]any{
		"url": url, "includeLinks": false, "includeImages": false,
		"useMainContentOnly": true, "timeoutMS": 60_000,
	}
}

// parseIndexFundScrapeMarkdown mirrors index_fund.parse_scrape_markdown.
// Unlike interestrates.go's parseInterestRateScrapeMarkdown, this does NOT
// validate contentLength - the Python source's index_fund.py version omits
// that check (only interest_rates.py's version has it); this is a
// deliberate, small duplication rather than a shared helper.
func parseIndexFundScrapeMarkdown(raw json.RawMessage, expectedURL string) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape payload is not an object"}
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape payload is not an object"}
	}
	if data, ok := payload["data"].(map[string]any); ok {
		payload = data
	}
	if success, ok := payload["success"].(bool); !ok || !success {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape did not report success"}
	}
	markdown, ok := payload["markdown"].(string)
	if !ok || strings.TrimSpace(markdown) == "" {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape returned empty markdown"}
	}
	returnedURL, ok := payload["url"].(string)
	if !ok || returnedURL != expectedURL {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape returned a different page URL"}
	}
	return markdown, nil
}

// --- markdown holdings table parsing ---

var (
	tickerCellRE  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,19}$`)
	cusipCellRE   = regexp.MustCompile(`^[A-Za-z0-9]{9}$`)
	isinCellRE    = regexp.MustCompile(`^[A-Za-z0-9]{12}$`)
	headerCleanRE = regexp.MustCompile(`[^a-z0-9%]+`)

	monthAltFund = "january|february|march|april|may|june|july|august|september|october|november|december"
	fundDateUSRE = regexp.MustCompile(`(?i)(` + monthAltFund + `)\s+(\d{1,2}),?\s+(\d{4})`)
	fundAsOfRE   = regexp.MustCompile(`(?i)as\s+of\s+(?:the\s+)?(?:quarter\s+)?(?:ended\s+)?([A-Za-z]+)\s+(\d{1,2}),?\s+(\d{4})`)
)

// headerAliasColumns is the ordered column list _header_key checks,
// mirroring index_fund._HEADER_ALIASES's declared order.
var headerAliasColumns = []struct {
	column  string
	aliases []string
}{
	{"ticker", []string{"ticker", "symbol", "symbols", "ticker symbol", "fund ticker"}},
	{"name", []string{"name", "company", "security", "security name", "holding", "description", "fund name", "issuer"}},
	{"cusip", []string{"cusip", "cusip number"}},
	{"isin", []string{"isin"}},
	{"weight", []string{"weight", "weight (%)", "% weight", "portfolio weight", "fund weight",
		"percentage of fund", "% of fund", "net assets (%)", "index weight"}},
	{"market_value", []string{"market value", "market value ($)", "value", "market value (usd)",
		"net assets", "amount", "market value usd"}},
	{"shares", []string{"shares", "shares held", "quantity", "share count", "number of shares"}},
}

func tableRows(markdown string) []string {
	var rows []string
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimRight(strings.TrimSpace(line), "\r")
		if strings.HasPrefix(trimmed, "|") && strings.Count(trimmed, "|") >= 3 {
			rows = append(rows, trimmed)
		}
	}
	return rows
}

func splitTableRow(line string) []string {
	trimmed := strings.Trim(strings.TrimSpace(line), "|")
	parts := strings.Split(trimmed, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

func headerKey(header string) string {
	normalized := headerCleanRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(header)), " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	for _, col := range headerAliasColumns {
		for _, alias := range col.aliases {
			if normalized == alias || (normalized != "" && strings.HasPrefix(normalized, alias)) {
				return col.column
			}
		}
	}
	return ""
}

func findTableHeader(rows []string) (int, map[string]int) {
	bestIndex := -1
	bestMapping := map[string]int{}
	limit := len(rows)
	if limit > 8 {
		limit = 8
	}
	for index := 0; index < limit; index++ {
		cells := splitTableRow(rows[index])
		mapping := map[string]int{}
		for column, header := range cells {
			key := headerKey(header)
			if key == "" {
				continue
			}
			if _, exists := mapping[key]; !exists {
				mapping[key] = column
			}
		}
		if len(mapping) > len(bestMapping) {
			bestIndex, bestMapping = index, mapping
		}
	}
	if bestIndex < 0 || len(bestMapping) == 0 {
		return -1, nil
	}
	return bestIndex, bestMapping
}

func fundCell(value string) *string {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return nil
	}
	return &cleaned
}

func fundTicker(value *string) *string {
	if value == nil {
		return nil
	}
	upper := strings.ToUpper(*value)
	if tickerCellRE.MatchString(upper) {
		return &upper
	}
	return nil
}

func fundIdentifier(value *string, pattern *regexp.Regexp) *string {
	if value == nil {
		return nil
	}
	upper := strings.ToUpper(*value)
	if pattern.MatchString(upper) {
		return &upper
	}
	return nil
}

func isTotalRow(row map[string]*string) bool {
	name := ""
	if row["name"] != nil {
		name = strings.ToLower(strings.TrimSpace(*row["name"]))
	}
	ticker := ""
	if row["ticker"] != nil {
		ticker = strings.ToUpper(strings.TrimSpace(*row["ticker"]))
	}
	return name == "total" || ticker == "TOTAL"
}

// isDecoratorRow mirrors index_fund._is_decorator_row: a row made only of
// markdown table separator characters (or with no non-blank cells at all)
// is skipped.
func isDecoratorRow(row map[string]*string) bool {
	for _, cell := range row {
		if cell == nil {
			continue
		}
		trimmed := strings.TrimSpace(*cell)
		if trimmed == "" {
			continue
		}
		if strings.Trim(trimmed, "-=: ") != "" {
			return false
		}
	}
	return true
}

func fundNumber(value *string) *float64 {
	if value == nil {
		return nil
	}
	text := strings.TrimSpace(*value)
	if text == "" {
		return nil
	}
	switch strings.ToLower(text) {
	case "-", "--", "n/a", "null", "none":
		return nil
	}
	negative := strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")")
	cleaned := strings.Trim(text, "()$%, ")
	multiplier := 1.0
	if cleaned != "" {
		last := cleaned[len(cleaned)-1:]
		switch last {
		case "B", "b":
			multiplier = 1_000_000_000.0
			cleaned = cleaned[:len(cleaned)-1]
		case "M", "m":
			multiplier = 1_000_000.0
			cleaned = cleaned[:len(cleaned)-1]
		case "K", "k":
			multiplier = 1_000.0
			cleaned = cleaned[:len(cleaned)-1]
		}
	}
	cleaned = strings.ReplaceAll(cleaned, ",", "")
	parsed, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return nil
	}
	result := parsed * multiplier
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return nil
	}
	if negative {
		result = -result
	}
	return &result
}

func deriveAssetClass(ticker, name, cusip *string) string {
	haystack := strings.ToLower(strPtrOr(ticker) + " " + strPtrOr(name) + " " + strPtrOr(cusip))
	bondWords := []string{"bond", "treasury", "note", "debenture", "agency", "mortgage", "government",
		"money market", "corporate", "municipal"}
	for _, w := range bondWords {
		if strings.Contains(haystack, w) {
			return "bond"
		}
	}
	if ticker != nil {
		return "equity"
	}
	return "other"
}

func strPtrOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// parseFundHoldings mirrors index_fund.parse_holdings.
func parseFundHoldings(markdown string) []fd.FundHolding {
	rows := tableRows(markdown)
	if len(rows) == 0 {
		return nil
	}
	headerIndex, columnMap := findTableHeader(rows)
	if headerIndex < 0 {
		return nil
	}
	_, hasTicker := columnMap["ticker"]
	_, hasName := columnMap["name"]
	if !hasTicker && !hasName {
		return nil
	}

	type dedupeKey struct{ ticker, cusip string }
	seen := map[dedupeKey]bool{}
	var records []fd.FundHolding
	for _, rawRow := range rows[headerIndex+1:] {
		cells := splitTableRow(rawRow)
		if len(cells) < 3 {
			continue
		}
		row := map[string]*string{}
		for column, index := range columnMap {
			if index < len(cells) {
				row[column] = fundCell(cells[index])
			}
		}
		ticker := fundTicker(row["ticker"])
		name := row["name"]
		cusip := fundIdentifier(row["cusip"], cusipCellRE)
		if ticker == nil && name == nil && row["cusip"] == nil {
			continue
		}
		if isTotalRow(row) {
			continue
		}
		if isDecoratorRow(row) {
			continue
		}
		weight := fundNumber(row["weight"])
		marketValue := fundNumber(row["market_value"])
		shares := fundNumber(row["shares"])
		isin := fundIdentifier(row["isin"], isinCellRE)
		key := dedupeKey{}
		if ticker != nil {
			key.ticker = *ticker
		}
		if cusip != nil {
			key.cusip = *cusip
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		assetClass := deriveAssetClass(ticker, name, cusip)
		records = append(records, fd.FundHolding{
			Ticker: ticker, Name: name, CUSIP: cusip, ISIN: isin,
			Weight: weight, MarketValue: marketValue, Shares: shares, AssetClass: &assetClass,
		})
	}
	sort.SliceStable(records, func(i, j int) bool { return fundHoldingLess(records[i], records[j]) })
	return records
}

// fundHoldingLess mirrors index_fund._holding_sort_key: records with a
// numeric weight sort first, by weight descending; the rest sort after,
// all ties broken by ticker-or-name ascending.
func fundHoldingLess(a, b fd.FundHolding) bool {
	aHas, bHas := a.Weight != nil, b.Weight != nil
	if aHas != bHas {
		return aHas
	}
	if aHas && *a.Weight != *b.Weight {
		return *a.Weight > *b.Weight
	}
	return fundHoldingLabel(a) < fundHoldingLabel(b)
}

func fundHoldingLabel(h fd.FundHolding) string {
	if h.Ticker != nil {
		return *h.Ticker
	}
	if h.Name != nil {
		return *h.Name
	}
	return ""
}

// parseFundAsOf mirrors index_fund.parse_as_of.
func parseFundAsOf(markdown string) *string {
	if m := fundAsOfRE.FindStringSubmatch(markdown); m != nil {
		return fundISODate(m[1], m[2], m[3])
	}
	if m := fundDateUSRE.FindStringSubmatch(markdown); m != nil {
		return fundISODate(m[1], m[2], m[3])
	}
	return nil
}

func fundISODate(month, day, year string) *string {
	monthNumber, ok := monthNames[strings.ToLower(month)]
	if !ok {
		return nil
	}
	parsedDay, err := strconv.Atoi(day)
	if err != nil || parsedDay < 1 || parsedDay > 31 {
		return nil
	}
	parsedYear, err := strconv.Atoi(year)
	if err != nil {
		return nil
	}
	s := isoDateString(parsedYear, monthNumber, parsedDay)
	return &s
}

// validateAssetClass mirrors index_fund.validate_asset_class.
func validateAssetClass(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(*value))
	if normalized != "equity" && normalized != "bond" {
		return nil, &providers.InputError{Msg: "asset_class must be equity or bond"}
	}
	return &normalized, nil
}
