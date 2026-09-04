// Current policy rates from three central-bank pages, fetched through
// Context.dev /web/scrape/markdown and parsed by the shape each bank
// publishes: the Fed and the ECB list decisions in a table, newest first;
// the BOE states the rate under a heading beside the announcement it
// came from. A bank whose page cannot be fetched or parsed is omitted;
// rates and dates are never guessed from anything other than the page
// text. The Bank of Japan is not listed: it publishes decisions only as
// PDF statements, which the markdown scraper cannot read.
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

// bankRate is one parsed central-bank observation.
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

	// The Fed's open market operations page holds one table per year with
	// a decision, newest year first, under a "#### 2025" heading:
	//   | Date | Increase | Decrease | Level (%) |
	//   | December 11 | 0 | 25 | 3.50-3.75 |
	// The first row under the first heading is the current target.
	fedYearRE = regexp.MustCompile(`(?m)^#+\s*(20\d\d)\s*$`)
	fedRowRE  = regexp.MustCompile(`(?im)^\|\s*(` + monthAlt + `)\s+(\d{1,2})\*?\s*\|[^|\n]*\|[^|\n]*\|\s*(\d{1,2}\.\d{2})(?:\s*-\s*(\d{1,2}\.\d{2}))?\s*\|`)

	// The ECB's key rates table lists one decision per row, newest first:
	//   | **2026** | 17 Jun. | 2.25 | 2.40 | \- | 2.65 |
	// The third cell is the deposit facility rate, which the Governing
	// Council has steered policy through since March 2024 and which
	// Financial Datasets reports as the ECB rate.
	ecbRowRE = regexp.MustCompile(`(?m)^\|\s*\*\*(20\d\d)\*\*\s*\|\s*(\d{1,2})\s+([A-Za-z]{3})[a-z]*\.?\d*\s*\|\s*(\x{2212}|-)?(\d{1,2}\.\d{2})\s*\|`)

	// The BOE's monetary policy page states the rate as a heading,
	// "## Current Bank Rate 3.75%", and lists announcements further down,
	// each dated on the line before its title:
	//   30 July 2026
	//   ### Bank Rate maintained at 3.75% - July 2026
	// The first announcement is the decision the current rate came from.
	boeCurrentRE  = regexp.MustCompile(`(?i)current\s+bank\s+rate\s+(\d{1,2}\.\d{1,2})\s*%`)
	boeDecisionRE = regexp.MustCompile(`(?i)#+\s*bank\s+rate\s+(?:maintained|held|increased|raised|reduced|cut)\s+(?:at|to)\s+\d{1,2}\.\d{1,2}\s*%`)

	dateUSRE   = regexp.MustCompile(`(?i)(` + monthAlt + `)\s+(\d{1,2}),?\s+(\d{4})`)
	dateLongRE = regexp.MustCompile(`(?i)(\d{1,2})\s+(` + monthAlt + `)\s+(\d{4})`)
	dateISORE  = regexp.MustCompile(`(20\d{2})-(\d{2})-(\d{2})`)
)

// parsePolicyRate reads one bank's page. It returns nil, not an error,
// when the page does not carry the rate in the shape that bank publishes.
func parsePolicyRate(markdown, bank string) *bankRate {
	spec := bankSpecByCode(bank)
	if spec == nil {
		return nil
	}
	var rate *float64
	var date *string
	switch bank {
	case "FED":
		rate, date = fedTable(markdown)
	case "ECB":
		rate, date = ecbTable(markdown)
	case "BOE":
		rate, date = boeProse(markdown)
	}
	if rate == nil {
		return nil
	}
	return &bankRate{Bank: spec.Bank, Name: spec.Name, Rate: *rate, Date: date}
}

func bankSpecByCode(bank string) *bankSpec {
	for i := range bankSpecs {
		if bankSpecs[i].Bank == bank {
			return &bankSpecs[i]
		}
	}
	return nil
}

// fedTable reads the first decision row under the newest year heading.
// A range such as 3.50-3.75 is reported as its midpoint, as Financial
// Datasets does.
func fedTable(markdown string) (*float64, *string) {
	year := fedYearRE.FindStringSubmatchIndex(markdown)
	if year == nil {
		return nil, nil
	}
	row := fedRowRE.FindStringSubmatch(markdown[year[1]:])
	if row == nil {
		return nil, nil
	}
	rate, err := strconv.ParseFloat(row[3], 64)
	if err != nil {
		return nil, nil
	}
	if row[4] != "" {
		upper, err := strconv.ParseFloat(row[4], 64)
		if err != nil {
			return nil, nil
		}
		rate = (rate + upper) / 2
	}
	return &rate, monthDayYearToISO(row[1], row[2], markdown[year[2]:year[3]])
}

// ecbTable reads the newest row of the key rates table.
func ecbTable(markdown string) (*float64, *string) {
	row := ecbRowRE.FindStringSubmatch(markdown)
	if row == nil {
		return nil, nil
	}
	rate, err := strconv.ParseFloat(row[5], 64)
	if err != nil {
		return nil, nil
	}
	if row[4] != "" {
		rate = -rate
	}
	return &rate, monthDayYearToISO(row[3], row[2], row[1])
}

// boeProse reads the "Current Bank Rate" heading and dates it from the
// first decision announcement below it.
func boeProse(markdown string) (*float64, *string) {
	current := boeCurrentRE.FindStringSubmatch(markdown)
	if current == nil {
		return nil, nil
	}
	rate, err := strconv.ParseFloat(current[1], 64)
	if err != nil {
		return nil, nil
	}
	var date *string
	if loc := boeDecisionRE.FindStringIndex(markdown); loc != nil {
		date = dateBefore(markdown, loc[0])
	}
	return &rate, date
}

// dateBeforeWindow bounds how far back dateBefore looks for the date a
// heading is announced under: a few lines, never another section.
const dateBeforeWindow = 300

// dateBefore returns the date written closest before position, in any of
// the US, long or ISO forms, as an ISO string.
func dateBefore(text string, position int) *string {
	lo := position - dateBeforeWindow
	if lo < 0 {
		lo = 0
	}
	window := text[lo:position]
	var found *string
	end := -1
	for _, m := range dateUSRE.FindAllStringSubmatchIndex(window, -1) {
		if m[1] > end {
			if d := monthDayYearToISO(window[m[2]:m[3]], window[m[4]:m[5]], window[m[6]:m[7]]); d != nil {
				found, end = d, m[1]
			}
		}
	}
	for _, m := range dateLongRE.FindAllStringSubmatchIndex(window, -1) {
		if m[1] > end {
			if d := monthDayYearToISO(window[m[4]:m[5]], window[m[2]:m[3]], window[m[6]:m[7]]); d != nil {
				found, end = d, m[1]
			}
		}
	}
	for _, m := range dateISORE.FindAllStringSubmatchIndex(window, -1) {
		if m[1] > end {
			d := window[m[0]:m[1]]
			found, end = &d, m[1]
		}
	}
	return found
}

// monthNumber accepts a full month name or its three-letter abbreviation.
func monthNumber(name string) (int, bool) {
	name = strings.ToLower(name)
	for full, n := range monthNames {
		if name == full || (len(name) == 3 && strings.HasPrefix(full, name)) {
			return n, true
		}
	}
	return 0, false
}

func monthDayYearToISO(month, day, year string) *string {
	monthNumber, ok := monthNumber(month)
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
