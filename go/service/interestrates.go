// Current policy rates from four central banks, each read the way that
// bank publishes it. The Fed and the ECB list decisions in a table on
// one page, newest first; the BOE states the rate under a heading beside
// the announcement it came from; the BOJ publishes each decision only as
// a PDF statement, so its releases listing supplies the newest statement
// and its date, and Context.dev's extractor reads the rate out of the
// PDF. A bank whose page cannot be fetched or parsed is omitted; rates
// and dates are never guessed from anything other than the page text.
package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/providers"
)

// bankSpec names one central bank and how its current rate is read.
type bankSpec struct {
	Bank string
	Name string
	// URL is the page read; for the BOJ it is a format string taking the
	// year of the releases listing.
	URL string
	// Read returns every decision the bank publishes, newest first.
	Read func(c *callCtx, spec bankSpec) ([]bankRate, error)
}

// bankSpecs is the coverage list; /macro/interest-rates/banks derives from it.
var bankSpecs = []bankSpec{
	{Bank: "FED", Name: "Federal Reserve", URL: "https://www.federalreserve.gov/monetarypolicy/openmarket.htm", Read: readScrapedRate},
	{Bank: "ECB", Name: "European Central Bank", URL: "https://www.ecb.europa.eu/stats/policy_and_exchange_rates/" +
		"key_ecb_interest_rates/html/index.en.html", Read: readScrapedRate},
	{Bank: "BOE", Name: "Bank of England", URL: "https://www.bankofengland.co.uk/monetary-policy", Read: readScrapedRate},
	{Bank: "BOJ", Name: "Bank of Japan", URL: "https://www.boj.or.jp/en/mopo/mpmdeci/mpr_%d/index.htm", Read: readBOJRate},
}

// interestRateScrapeQuery is the Context.dev markdown scrape for one page;
// links are kept only where the page's links are the data.
func interestRateScrapeQuery(url string, links bool) map[string]any {
	return map[string]any{
		"url": url, "includeLinks": links, "includeImages": false,
		"useMainContentOnly": true, "timeoutMS": 60_000,
	}
}

// readScrapedRate reads a bank whose page states its decisions in the text.
func readScrapedRate(c *callCtx, spec bankSpec) ([]bankRate, error) {
	run, err := c.run(contextDev, scrapeEndpoint, nil, interestRateScrapeQuery(spec.URL, false))
	if err != nil {
		return nil, err
	}
	markdown, err := parseInterestRateScrapeMarkdown(run.Output, spec.URL)
	if err != nil {
		return nil, err
	}
	if history := interestRateHistory(markdown, spec.Bank, spec.Name); len(history) > 0 {
		return history, nil
	}
	// A page whose table did not parse can still state a current rate.
	rate, date := parsePolicyRate(markdown, spec.Bank)
	if rate == nil {
		return nil, nil
	}
	return []bankRate{{Bank: spec.Bank, Name: spec.Name, Rate: *rate, Date: date}}, nil
}

// bojStatementRE matches one row of the BOJ releases table that links a
// Statement on Monetary Policy, newest first:
//
//	| July 31, 2026 | [Statement on Monetary Policy \[PDF 160KB\]](https://.../k260731a.pdf) |
var bojStatementRE = regexp.MustCompile(`(?m)^\|\s*([A-Za-z]{3,9})\.?\s+(\d{1,2}),\s+(\d{4})\s*\|\s*\[Statement on Monetary Policy.*?\]\((https?://\S+?\.pdf)\)`)

// bojStatementRow finds the newest statement row. The page sets its dates
// with no-break spaces, which Go's \s does not match, so they are
// normalised first.
func bojStatementRow(markdown string) []string {
	return bojStatementRE.FindStringSubmatch(strings.ReplaceAll(markdown, "\u00a0", " "))
}

var bojExtractSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"rate_percent": map[string]any{
			"type":        "number",
			"description": "The short-term policy interest rate this statement sets (the uncollateralized overnight call rate target), as a percent, for example 0.75.",
		},
	},
	"required": []string{"rate_percent"},
}

const bojExtractInstructions = "Read this Bank of Japan Statement on Monetary Policy. Return the short-term policy interest rate it sets, the uncollateralized overnight call rate target, as a percent number. If the statement sets no such rate, return null."

// readBOJRate lists the year's releases, takes the newest Statement on
// Monetary Policy and its date, and has the extractor read the rate from
// the PDF. In January the new year's listing may hold no statement yet,
// so the previous year is tried next.
func readBOJRate(c *callCtx, spec bankSpec) ([]bankRate, error) {
	year := time.Now().UTC().Year()
	for _, y := range []int{year, year - 1} {
		url := fmt.Sprintf(spec.URL, y)
		run, err := c.run(contextDev, scrapeEndpoint, nil, interestRateScrapeQuery(url, true))
		if err != nil {
			return nil, err
		}
		markdown, err := parseInterestRateScrapeMarkdown(run.Output, url)
		if err != nil {
			return nil, err
		}
		statement := bojStatementRow(markdown)
		if statement == nil {
			continue
		}
		rate, err := c.extractBOJRate(statement[4])
		if err != nil {
			return nil, err
		}
		return []bankRate{{Bank: spec.Bank, Name: spec.Name, Rate: rate,
			Date: monthDayYearToISO(statement[1], statement[2], statement[3])}}, nil
	}
	return nil, nil
}

// extractBOJRate reads the policy rate out of one statement PDF.
func (c *callCtx) extractBOJRate(pdfURL string) (float64, error) {
	run, err := c.run(contextDev, extractEndpoint, extractRequestBody(pdfURL, bojExtractSchema, bojExtractInstructions), nil)
	if err != nil {
		return 0, err
	}
	data, err := parseExtractOutput(run.Output)
	if err != nil {
		return 0, err
	}
	rate, ok := data["rate_percent"].(float64)
	if !ok || rate < -1 || rate > 10 {
		return 0, &providers.SchemaDriftError{Msg: "BOJ statement extract did not yield a policy rate percent"}
	}
	return rate, nil
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

// boeAnnouncementRE matches one Bank Rate announcement heading, which
// carries the rate it set:
//
//	### Bank Rate maintained at 3.75% - July 2026
var boeAnnouncementRE = regexp.MustCompile(`(?i)#+\s*bank\s+rate\s+(?:maintained|held|increased|raised|reduced|cut)\s+(?:at|to)\s+(\d{1,2}\.\d{1,2})\s*%`)

// interestRateHistory returns every decision the bank's page carries, in
// the order the page lists them. One scrape already contains it; reading
// only the newest row threw the rest away.
func interestRateHistory(markdown, bank, name string) []bankRate {
	var out []bankRate
	add := func(rate float64, date *string) {
		if date == nil {
			return
		}
		out = append(out, bankRate{Bank: bank, Name: name, Rate: rate, Date: date})
	}
	switch bank {
	case "FED":
		// One table per year, under a "#### 2025" heading.
		years := fedYearRE.FindAllStringSubmatchIndex(markdown, -1)
		for i, y := range years {
			end := len(markdown)
			if i+1 < len(years) {
				end = years[i+1][0]
			}
			year := markdown[y[2]:y[3]]
			for _, row := range fedRowRE.FindAllStringSubmatch(markdown[y[1]:end], -1) {
				rate, err := strconv.ParseFloat(row[3], 64)
				if err != nil {
					continue
				}
				if row[4] != "" {
					upper, uerr := strconv.ParseFloat(row[4], 64)
					if uerr != nil {
						continue
					}
					rate = (rate + upper) / 2
				}
				add(rate, monthDayYearToISO(row[1], row[2], year))
			}
		}
	case "ECB":
		// Rows are newest first and only the first row of a year carries
		// the year, so it is carried forward.
		year := ""
		for _, row := range ecbRowRE.FindAllStringSubmatch(markdown, -1) {
			if row[1] != "" {
				year = row[1]
			}
			if year == "" {
				continue
			}
			rate, err := strconv.ParseFloat(row[5], 64)
			if err != nil {
				continue
			}
			if row[4] != "" {
				rate = -rate
			}
			add(rate, monthDayYearToISO(row[3], row[2], year))
		}
	case "BOE":
		for _, loc := range boeAnnouncementRE.FindAllStringSubmatchIndex(markdown, -1) {
			rate, err := strconv.ParseFloat(markdown[loc[2]:loc[3]], 64)
			if err != nil {
				continue
			}
			add(rate, dateBefore(markdown, loc[0]))
		}
	}
	return out
}

// monthlySeries samples a decision history at the first of each month in
// [from, to], which is the shape Financial Datasets publishes. A month
// before the first decision on the page has no answer and is skipped
// rather than back-filled with a guess.
func monthlySeries(history []bankRate, from, to time.Time) []bankRate {
	type point struct {
		day  time.Time
		rate bankRate
	}
	decisions := make([]point, 0, len(history))
	for _, h := range history {
		day, err := time.Parse(dateLayout, *h.Date)
		if err != nil {
			continue
		}
		decisions = append(decisions, point{day: day, rate: h})
	}
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].day.Before(decisions[j].day) })

	var out []bankRate
	for m := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.UTC); !m.After(to); m = m.AddDate(0, 1, 0) {
		var inForce *bankRate
		for i := range decisions {
			if decisions[i].day.After(m) {
				break
			}
			inForce = &decisions[i].rate
		}
		if inForce == nil {
			continue
		}
		day := m.Format(dateLayout)
		out = append(out, bankRate{Bank: inForce.Bank, Name: inForce.Name, Rate: inForce.Rate, Date: &day})
	}
	return out
}

// parsePolicyRate reads one bank's page. A nil rate, not an error, means
// the page does not carry the rate in the shape that bank publishes.
func parsePolicyRate(markdown, bank string) (rate *float64, date *string) {
	switch bank {
	case "FED":
		return fedTable(markdown)
	case "ECB":
		return ecbTable(markdown)
	case "BOE":
		return boeProse(markdown)
	}
	return nil, nil
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

// interestRateWindow is the default reporting window: the rate in force at
// the start of each of the last twelve months, ending with the most recent
// complete month. It is the shape Financial Datasets publishes.
func interestRateWindow(now time.Time) (from, to time.Time) {
	to = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0)
	return to.AddDate(0, -10, 0), to
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

// monthNumber accepts a full month name or any abbreviation of three or more letters.
func monthNumber(name string) (int, bool) {
	name = strings.ToLower(name)
	for full, n := range monthNames {
		if name == full || (len(name) >= 3 && strings.HasPrefix(full, name)) {
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
