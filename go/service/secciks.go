// This file builds the two CIK enumeration capabilities, list_filings_ciks
// and list_company_facts_ciks, from SEC EDGAR's own published company
// ticker file (https://www.sec.gov/files/company_tickers.json).
//
// The file is fetched through Monid's context.dev scraper rather than by
// a direct HTTP call, for the same reasons every other upstream call in
// this package goes through Monid: the request is billed to the caller's
// own wallet, it writes a receipts-ledger entry, and it is checked
// against the discovery allowlist. A direct fetch would bypass all three.
// It also removes an operator burden: sec.gov answers 403 to any
// User-Agent without a contact email, so a direct fetch made every
// deployment declare one. The scraper handles that.
//
// Measured 2026-09-04: the scraper returned the file byte-intact, 922KB
// with every row and cik_str preserved, for $0.0009. It returns raw
// content rather than rendered markdown, so the JSON parses unchanged.
//
// Provenance: Financial Datasets' /filings/ciks response was captured
// live the same day and compared against this file. The two are
// byte-identical - same 10,412 entries, same order, same unpadded
// formatting - which is why this port can reproduce that route exactly
// without redistributing any Financial Datasets data.
//
// /company/facts/ciks is a different case and is NOT exact: Financial
// Datasets returns 21,005 distinct zero-padded CIKs there, a superset of
// the 8,005 in the ticker file, because it includes filers that have no
// listed ticker. This port serves the ticker-file universe zero-padded
// and says so, rather than padding the list with CIKs it cannot source.
package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/belazy/monid-finance/providers"
)

// secCompanyTickersURL is a var, not a const, so tests can point it at an
// httptest server and exercise the parser without reaching sec.gov.
var secCompanyTickersURL = "https://www.sec.gov/files/company_tickers.json"

const (
	// secCatalogTTL bounds how long one fetched copy is reused. The file
	// changes at most daily.
	secCatalogTTL = 6 * time.Hour
)

// secTickerRow is one entry of SEC's company_tickers.json. The file is a
// JSON object keyed by row index, not an array.
type secTickerRow struct {
	CIK    int64  `json:"cik_str"`
	Ticker string `json:"ticker"`
	Title  string `json:"title"`
}

// secCatalog is one fetched copy of the SEC ticker file, in file order.
type secCatalog struct {
	rows      []secTickerRow
	fetchedAt time.Time
}

var (
	secCatalogMu    sync.Mutex
	secCatalogCache *secCatalog
)

// resetSECCatalogCache drops the cached copy. Tests use it so one test's
// fetch cannot satisfy the next one's assertions.
func resetSECCatalogCache() {
	secCatalogMu.Lock()
	defer secCatalogMu.Unlock()
	secCatalogCache = nil
}

// fetchSECCatalog returns the SEC ticker file, reusing a copy fetched
// inside secCatalogTTL. The lock is held across the fetch so a burst of
// concurrent callers performs one download rather than one each.
func (c *callCtx) fetchSECCatalog() ([]secTickerRow, error) {
	secCatalogMu.Lock()
	defer secCatalogMu.Unlock()
	if secCatalogCache != nil && time.Since(secCatalogCache.fetchedAt) < secCatalogTTL {
		return secCatalogCache.rows, nil
	}

	run, err := c.run(contextDev, scrapeHTMLEndpoint, nil, map[string]any{"url": secCompanyTickersURL})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success bool   `json:"success"`
		HTML    string `json:"html"`
	}
	if err := json.Unmarshal(run.Output, &envelope); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "context.dev scrape payload must be an object"}
	}
	if !envelope.Success || envelope.HTML == "" {
		return nil, &providers.SchemaDriftError{Msg: "context.dev returned no content for the SEC company-ticker file"}
	}
	body := []byte(envelope.HTML)

	// The file is an object keyed by row index ("0", "1", ...). Go maps
	// have no order, so rows are re-sorted by that numeric key to recover
	// SEC's own ordering, which Financial Datasets preserves too.
	var keyed map[string]secTickerRow
	if err := json.Unmarshal(body, &keyed); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "SEC company-ticker file is not the expected index-keyed object"}
	}
	if len(keyed) == 0 {
		return nil, &providers.SchemaDriftError{Msg: "SEC company-ticker file carried no rows"}
	}
	rows := make([]secTickerRow, 0, len(keyed))
	for i := 0; i < len(keyed); i++ {
		row, ok := keyed[strconv.Itoa(i)]
		if !ok {
			return nil, &providers.SchemaDriftError{
				Msg: fmt.Sprintf("SEC company-ticker file is missing row %d; its index keys are no longer contiguous", i)}
		}
		rows = append(rows, row)
	}

	secCatalogCache = &secCatalog{rows: rows, fetchedAt: time.Now()}
	return rows, nil
}

// listFilingsCIKs answers list_filings_ciks: every CIK in SEC's ticker
// file, in file order, unpadded. Verified byte-identical to Financial
// Datasets' own /filings/ciks response on 2026-09-04. Duplicates are kept
// deliberately: a company with several share classes appears once per
// ticker there, and Financial Datasets keeps them too.
func (c *callCtx) listFilingsCIKs(args map[string]any) (Result, error) {
	rows, err := c.fetchSECCatalog()
	if err != nil {
		return Result{}, err
	}
	ciks := make([]any, len(rows))
	for i, row := range rows {
		ciks[i] = strconv.FormatInt(row.CIK, 10)
	}
	out := newOrderedJSONObject()
	out.set("resource", "filings")
	out.set("ciks", ciks)
	return Result{Value: out}, nil
}

// listCompanyFactsCIKs answers list_company_facts_ciks: the same universe,
// deduplicated and zero-padded to 10 digits, sorted ascending. This is
// deliberately NOT a claim of parity with Financial Datasets' own list,
// which is a superset covering filers with no listed ticker (see the file
// doc comment); it is the CIK universe this server can actually resolve
// company facts for.
func (c *callCtx) listCompanyFactsCIKs(args map[string]any) (Result, error) {
	rows, err := c.fetchSECCatalog()
	if err != nil {
		return Result{}, err
	}
	seen := make(map[string]bool, len(rows))
	padded := make([]string, 0, len(rows))
	for _, row := range rows {
		cik := fmt.Sprintf("%010d", row.CIK)
		if seen[cik] {
			continue
		}
		seen[cik] = true
		padded = append(padded, cik)
	}
	sort.Strings(padded)
	ciks := make([]any, len(padded))
	for i, cik := range padded {
		ciks[i] = cik
	}
	out := newOrderedJSONObject()
	out.set("resource", "company_facts")
	out.set("ciks", ciks)
	return Result{Value: out}, nil
}
