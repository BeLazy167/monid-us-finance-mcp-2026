// The two coverage catalogs that used to answer a not_implemented stub:
// the sector taxonomy behind /kpi/metrics/sectors and the fund universe
// behind /index-funds/tickers. Both are scraped live through Monid rather
// than embedded, so each answers from a source that can be checked and
// neither copies another vendor's curated list.
package service

import (
	"regexp"
	"sort"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

const (
	// finvizIndustriesURL lists every industry the screener recognises,
	// which is the taxonomy a KPI request can be scoped by.
	finvizIndustriesURL = "https://finviz.com/groups.ashx?g=industry&v=110"
	// etfUniverseURL lists exchange-traded funds with their ticker.
	etfUniverseURL = "https://stockanalysis.com/etf/"
)

var (
	// A data row is numbered and links the group's own page:
	//   | 1 | [Advertising Agencies](https://finviz.com/groups...) | 34 | ...
	finvizIndustryRE = regexp.MustCompile(`(?m)^\|\s*\d+\s*\|\s*\[([^\]]{2,60})\]\(`)
	// Each fund links its own page, from which the ticker is read:
	//   | [AAA](https://stockanalysis.com/etf/aaa/) | Alternative Access ...
	etfTickerRE = regexp.MustCompile(`\[([A-Z][A-Z0-9.]{0,6})\]\(https://stockanalysis\.com/etf/[a-z0-9.]+/\)`)
)

// slugify renders a display name as the lower-case underscore form these
// catalogs publish: "Auto & Truck Dealerships" becomes
// "auto_truck_dealerships".
func slugify(name string) string {
	var b strings.Builder
	lastUnderscore := true
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// sortedUnique returns the distinct values, in order, as an any slice
// ready for a JSON array.
func sortedUnique(values []string) []any {
	seen := make(map[string]bool, len(values))
	kept := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		kept = append(kept, v)
	}
	sort.Strings(kept)
	out := make([]any, len(kept))
	for i, v := range kept {
		out[i] = v
	}
	return out
}

// scrapeMarkdown fetches one page through Monid and returns its markdown.
func (c *callCtx) scrapeMarkdown(url string) (string, error) {
	run, err := c.run(contextDev, scrapeEndpoint, nil, interestRateScrapeQuery(url, true))
	if err != nil {
		return "", err
	}
	return parseInterestRateScrapeMarkdown(run.Output, url)
}

// listKPISectors answers /kpi/metrics/sectors with the industry taxonomy
// the screener provider recognises, scraped live. It is that provider's
// taxonomy, not a copy of anyone else's curated list, and it is the same
// vocabulary the screener itself accepts.
func (c *callCtx) listKPISectors(args map[string]any) (Result, error) {
	markdown, err := c.scrapeMarkdown(finvizIndustriesURL)
	if err != nil {
		return Result{}, err
	}
	names := finvizIndustryRE.FindAllStringSubmatch(markdown, -1)
	sectors := make([]string, 0, len(names))
	for _, m := range names {
		sectors = append(sectors, slugify(m[1]))
	}
	list := sortedUnique(sectors)
	if len(list) == 0 {
		return Result{}, &providers.SchemaDriftError{Msg: "the industry catalog page carried no industries"}
	}
	out := newOrderedJSONObject()
	out.set("resource", "kpi_metrics")
	out.set("sectors", list)
	return Result{Value: out}, nil
}

// listIndexFundTickers answers /index-funds/tickers with the funds the
// upstream universe lists, scraped live.
func (c *callCtx) listIndexFundTickers(args map[string]any) (Result, error) {
	markdown, err := c.scrapeMarkdown(etfUniverseURL)
	if err != nil {
		return Result{}, err
	}
	matches := etfTickerRE.FindAllStringSubmatch(markdown, -1)
	tickers := make([]string, 0, len(matches))
	for _, m := range matches {
		tickers = append(tickers, m[1])
	}
	list := sortedUnique(tickers)
	if len(list) == 0 {
		return Result{}, &providers.SchemaDriftError{Msg: "the fund universe page carried no tickers"}
	}
	out := newOrderedJSONObject()
	out.set("resource", "index_funds")
	out.set("tickers", list)
	return Result{Value: out}, nil
}
