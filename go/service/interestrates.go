// This file ports interest_rates.py: fetching current policy interest
// rates from four central-bank pages via Context.dev /web/scrape/markdown,
// and parsing each with a strict per-bank regular expression. A bank whose
// page cannot be fetched or parsed is omitted; rates and dates are never
// guessed from anything other than the page text.
package service

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/providers"
)

// bankSpec mirrors interest_rates.BankSpec.
type bankSpec struct {
	Bank string
	Name string
	URL  string
}

// bankSpecs mirrors interest_rates.BANK_SPECS exactly, including order.
var bankSpecs = []bankSpec{
	{Bank: "FED", Name: "Federal Reserve", URL: "https://www.federalreserve.gov/monetarypolicy/openmarket.htm"},
	{Bank: "ECB", Name: "European Central Bank", URL: "https://www.ecb.europa.eu/stats/policy_and_exchange_rates/" +
		"key_ecb_interest_rates/html/index.en.html"},
	{Bank: "BOE", Name: "Bank of England", URL: "https://www.bankofengland.co.uk/monetary-policy"},
	{Bank: "BOJ", Name: "Bank of Japan", URL: "https://www.boj.or.jp/en/mopo/mpr_2026/index.htm"},
}

// interestRateScrapeQuery mirrors interest_rates.scrape_query(url, timeout_ms=60_000).
func interestRateScrapeQuery(url string) map[string]any {
	return map[string]any{
		"url": url, "includeLinks": false, "includeImages": false,
		"useMainContentOnly": true, "timeoutMS": 60_000,
	}
}

// parseInterestRateScrapeMarkdown mirrors interest_rates.parse_scrape_markdown.
func parseInterestRateScrapeMarkdown(raw json.RawMessage, expectedURL string) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape payload is not an object"}
	}
	payload, ok := value.(map[string]any)
	if ok {
		if data, ok := payload["data"].(map[string]any); ok {
			payload = data
		}
	} else {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape payload is not an object"}
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
	contentLength, ok := payload["contentLength"]
	if !ok {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape contentLength must be a non-negative integer"}
	}
	if _, isBool := contentLength.(bool); isBool {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape contentLength must be a non-negative integer"}
	}
	f, ok := contentLength.(float64)
	if !ok || f < 0 || f != float64(int64(f)) {
		return "", &providers.SchemaDriftError{Msg: "Context.dev scrape contentLength must be a non-negative integer"}
	}
	return markdown, nil
}

// bankRate mirrors interest_rates.BankRate.
type bankRate struct {
	Bank string
	Name string
	Rate float64
	Date *string
}

var (
	monthNames = map[string]int{
		"january": 1, "february": 2, "march": 3, "april": 4, "may": 5, "june": 6,
		"july": 7, "august": 8, "september": 9, "october": 10, "november": 11, "december": 12,
	}
	monthAlt = "january|february|march|april|may|june|july|august|september|october|november|december"

	fractionRE    = regexp.MustCompile(`(\d+)-(\d)/(\d)`)
	fedRangeRE    = regexp.MustCompile(`(?i)federal\s+funds\s+rate[^\n]{0,220}?(\d{1,2}(?:\.\d{1,3})?)\s*(?:-|\x{2013}|\x{2014}|to)\s*(\d{1,2}(?:\.\d{1,3})?)\s*percent`)
	fedSingleRE   = regexp.MustCompile(`(?i)federal\s+funds\s+rate[^\n]{0,220}?(\d{1,2}(?:\.\d{1,3})?)\s*percent`)
	ecbRateRE     = regexp.MustCompile(`(?i)main\s+refinancing\s+operations[^\n]{0,110}?(\d{1,2}[.,]\d{1,2})\s*%`)
	boeRateRE     = regexp.MustCompile(`(?i)bank\s+rate[^\n]{0,130}?(\d{1,2}\.\d{1,2})\s*%`)
	bojRateRE     = regexp.MustCompile(`(?i)(?:short-term\s+policy\s+interest\s+rate|uncollateralized\s+overnight\s+call\s+rate|policy\s+rate)[^\n]{0,180}?(\d{1,2}(?:\.\d{1,2})?)\s*percent`)
	bojRateSignRE = regexp.MustCompile(`(?i)(?:short-term\s+policy\s+interest\s+rate|uncollateralized\s+overnight\s+call\s+rate|policy\s+rate)[^\n]{0,180}?(\d{1,2}(?:\.\d{1,2})?)\s*%`)

	dateUSRE   = regexp.MustCompile(`(?i)(` + monthAlt + `)\s+(\d{1,2}),?\s+(\d{4})`)
	dateLongRE = regexp.MustCompile(`(?i)(\d{1,2})\s+(` + monthAlt + `)\s+(\d{4})`)
	dateISORE  = regexp.MustCompile(`(20\d{2})-(\d{2})-(\d{2})`)
)

// expandFractions mirrors interest_rates._expand_fractions.
func expandFractions(markdown string) string {
	return fractionRE.ReplaceAllStringFunc(markdown, func(m string) string {
		parts := fractionRE.FindStringSubmatch(m)
		whole, _ := strconv.Atoi(parts[1])
		num, _ := strconv.Atoi(parts[2])
		den, _ := strconv.Atoi(parts[3])
		if den == 0 {
			return m
		}
		value := float64(whole) + float64(num)/float64(den)
		text := strconv.FormatFloat(value, 'f', 3, 64)
		text = strings.TrimRight(text, "0")
		text = strings.TrimRight(text, ".")
		return text
	})
}

// parsePolicyRate mirrors interest_rates.parse_policy_rate. Returns nil
// (not an error) when the bank is unknown or its page can't be parsed.
func parsePolicyRate(markdown, bank string) *bankRate {
	spec := bankSpecByCode(bank)
	if spec == nil {
		return nil
	}
	switch bank {
	case "FED":
		return fedRate(markdown, *spec)
	case "ECB":
		return singleRate(markdown, *spec, ecbRateRE)
	case "BOE":
		return singleRate(markdown, *spec, boeRateRE)
	case "BOJ":
		if r := singleRate(markdown, *spec, bojRateRE); r != nil {
			return r
		}
		return singleRate(markdown, *spec, bojRateSignRE)
	}
	return nil
}

func bankSpecByCode(bank string) *bankSpec {
	for i := range bankSpecs {
		if bankSpecs[i].Bank == bank {
			return &bankSpecs[i]
		}
	}
	return nil
}

func fedRate(markdown string, spec bankSpec) *bankRate {
	clean := expandFractions(markdown)
	if loc := fedRangeRE.FindStringSubmatchIndex(clean); loc != nil {
		m := fedRangeRE.FindStringSubmatch(clean)
		lower, _ := strconv.ParseFloat(m[1], 64)
		upper, _ := strconv.ParseFloat(m[2], 64)
		rate := (lower + upper) / 2.0
		return &bankRate{Bank: spec.Bank, Name: spec.Name, Rate: rate, Date: nearbyDate(clean, loc[0])}
	}
	if loc := fedSingleRE.FindStringSubmatchIndex(clean); loc != nil {
		m := fedSingleRE.FindStringSubmatch(clean)
		rate, _ := strconv.ParseFloat(m[1], 64)
		return &bankRate{Bank: spec.Bank, Name: spec.Name, Rate: rate, Date: nearbyDate(clean, loc[0])}
	}
	return nil
}

func singleRate(markdown string, spec bankSpec, pattern *regexp.Regexp) *bankRate {
	loc := pattern.FindStringSubmatchIndex(markdown)
	if loc == nil {
		return nil
	}
	m := pattern.FindStringSubmatch(markdown)
	raw := strings.ReplaceAll(m[1], ",", ".")
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &bankRate{Bank: spec.Bank, Name: spec.Name, Rate: rate, Date: nearbyDate(markdown, loc[0])}
}

// nearbyDate mirrors interest_rates._nearby_date: searches an 800-before/
// 400-after character window around position for a date in one of three
// formats (US, long, ISO, tried in that order, matching Python's
// `for pattern in (_DATE_US, _DATE_LONG, _DATE_ISO)`), keeping the LAST
// match found across all three patterns' occurrences in the window.
func nearbyDate(text string, position int) *string {
	start := position - 800
	if start < 0 {
		start = 0
	}
	end := position + 400
	if end > len(text) {
		end = len(text)
	}
	window := text[start:end]

	var candidates []string
	for _, m := range dateUSRE.FindAllStringSubmatch(window, -1) {
		if iso := monthDayYearToISO(m[1], m[2], m[3]); iso != nil {
			candidates = append(candidates, *iso)
		}
	}
	for _, m := range dateLongRE.FindAllStringSubmatch(window, -1) {
		if iso := monthDayYearToISO(m[2], m[1], m[3]); iso != nil {
			candidates = append(candidates, *iso)
		}
	}
	for _, m := range dateISORE.FindAllStringSubmatch(window, -1) {
		year, _ := strconv.Atoi(m[1])
		month, _ := strconv.Atoi(m[2])
		day, _ := strconv.Atoi(m[3])
		if month >= 1 && month <= 12 && day >= 1 && day <= 31 {
			s := isoDateString(year, month, day)
			candidates = append(candidates, s)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	last := candidates[len(candidates)-1]
	return &last
}

func monthDayYearToISO(month, day, year string) *string {
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

func isoDateString(year, month, day int) string {
	return time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("2006-01") + "-" + pad2(day)
}

func pad2(v int) string {
	if v < 10 {
		return "0" + strconv.Itoa(v)
	}
	return strconv.Itoa(v)
}
