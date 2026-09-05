// The earnings release behind get_kpi_guidance and get_kpi_non_gaap.
//
// A company declares its dividend and reconciles its non-GAAP measures in
// the press release it files as exhibit 99.1 to an 8-K, not in the 10-Q.
// Reading the 10-Q for either is reading the wrong document: measured on
// 2026-09-05, Apple's Q3 2026 10-Q yielded nothing for either tool, while
// the exhibit to the 8-K filed the same day carried the $0.27 dividend and
// the September 2025 exhibit carried four non-GAAP measures.
package service

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

// exhibitHrefRE matches an exhibit 99 document in an EDGAR filing
// directory listing, e.g. a8-kex991q3202606272026.htm.
var exhibitHrefRE = regexp.MustCompile(`(?i)href="([^"]*ex-?99[^"]*\.htm)"`)

// accessionDirRE splits a filing document URL into the directory holding
// every document of that filing.
var accessionDirRE = regexp.MustCompile(`^(https://www\.sec\.gov/Archives/edgar/data/[^/]+/[^/]+/)`)

// earningsExhibit returns the exhibit 99 document URL for one 8-K, or an
// empty string when the filing has none. It costs one scrape of the
// filing's own directory listing.
func (c *callCtx) earningsExhibit(filingURL string) (string, error) {
	dir := accessionDirRE.FindStringSubmatch(filingURL)
	if dir == nil {
		return "", nil
	}
	run, err := c.run(contextDev, scrapeHTMLEndpoint, nil, map[string]any{"url": dir[1]})
	if err != nil {
		return "", err
	}
	var envelope struct {
		Success bool   `json:"success"`
		HTML    string `json:"html"`
	}
	if jerr := json.Unmarshal(run.Output, &envelope); jerr != nil {
		return "", &providers.SchemaDriftError{Msg: "context.dev scrape payload must be an object"}
	}
	if !envelope.Success {
		return "", &providers.SchemaDriftError{Msg: "context.dev returned no content for the filing directory"}
	}
	match := exhibitHrefRE.FindStringSubmatch(envelope.HTML)
	if match == nil {
		return "", nil
	}
	href := match[1]
	if strings.HasPrefix(href, "http") {
		return href, nil
	}
	return "https://www.sec.gov" + href, nil
}

// maxEarningsExhibits bounds how far back a KPI request looks. Each
// exhibit costs a directory scrape plus an extraction, so the search
// stops as soon as it has enough items rather than walking every filing.
// Six covers about eighteen months of releases: a company reconciles
// non-GAAP measures only when it has a one-off to explain, and Apple's
// most recent was four releases back when this was measured.
const maxEarningsExhibits = 6

// kpiFromEarningsReleases extracts KPI items from recent earnings
// releases, newest first, stopping once it has enough for the request.
// Reporting only the newest exhibit would answer "none disclosed" for a
// company that reconciled its non-GAAP measures one quarter earlier,
// which is what reading a single filing did.
func (c *callCtx) kpiFromEarningsReleases(
	parsed kpiExtractArgs, schema map[string]any, instructions string,
) (data map[string]any, sourceURL string, found bool, err error) {
	filingsRun, rerr := c.run(defillama, filingsEndpoint, nil,
		map[string]any{"ticker": parsed.ticker, "country": "US"})
	if rerr != nil {
		return nil, "", false, rerr
	}
	filings, nerr := providers.NormalizeFilings(filingsRun.Output, parsed.ticker, nil, 10_000, nil, nil)
	if nerr != nil {
		return nil, "", false, nerr
	}

	var kpis []any
	opened := 0
	for i := range filings {
		if opened >= maxEarningsExhibits || len(kpis) >= parsed.limit {
			break
		}
		if filings[i].FilingType == nil || *filings[i].FilingType != "8-K" || filings[i].URL == nil {
			continue
		}
		exhibit, eerr := c.earningsExhibit(*filings[i].URL)
		if eerr != nil || exhibit == "" {
			continue
		}
		opened++
		found = true
		run, rerr := c.run(contextDev, extractEndpoint, extractRequestBody(exhibit, schema, instructions), nil)
		if rerr != nil {
			continue
		}
		extracted, perr := parseExtractOutput(run.Output)
		if perr != nil {
			continue
		}
		batch, ok := extracted["kpis"].([]any)
		if !ok || len(batch) == 0 {
			continue
		}
		if sourceURL == "" {
			sourceURL = exhibit
		}
		kpis = append(kpis, batch...)
	}
	return map[string]any{"kpis": kpis}, sourceURL, found, nil
}
