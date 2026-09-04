// This file builds the two CIK enumeration capabilities, list_filings_ciks
// and list_company_facts_ciks, from SEC EDGAR's own published company
// ticker file rather than from any Monid provider.
//
// Why this file makes a direct HTTP call, uniquely in this package: the
// source is https://www.sec.gov/files/company_tickers.json, which SEC
// publishes free and without authentication. Routing it through a paid
// Monid provider would bill the caller for data the SEC gives away, so
// this capability costs the caller nothing and writes no receipt (there
// is no measured cost to record). It is the only place go/service reaches
// the network outside monid.Client; every billable call still goes
// through callCtx.run.
//
// Provenance, measured 2026-09-04: Financial Datasets' /filings/ciks
// response was captured live and compared against this file. The two are
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
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
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
	// secMaxBody caps the download; the real file is around 800KB.
	secMaxBody = 16 << 20
)

// secUserAgent returns the operator-declared User-Agent, or an error
// naming exactly what to set.
//
// SEC's access policy requires a User-Agent carrying a real contact
// address, and it is enforced: measured 2026-09-04, sec.gov answered 403
// to a descriptive UA and to one carrying only a repository URL, and 200
// only once an email address was present.
//
// This deliberately has no default. Baking a placeholder address into an
// open-source server would send SEC an unreachable contact from every
// deployment that ever runs it, and make all of them indistinguishable in
// SEC's logs. Declaring a real address is the operator's call, the same
// way the Monid key is, so an unset value fails loudly with instructions
// rather than quietly misattributing traffic.
func secUserAgent() (string, error) {
	if v := strings.TrimSpace(os.Getenv("SEC_USER_AGENT")); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("SEC_USER_AGENT is not set: SEC requires a User-Agent containing a real contact " +
		"email before it will serve its public files (it answers 403 otherwise). " +
		`Set it to something like "Your Project Name you@example.com" and retry`)
}

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

	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, secCompanyTickersURL, nil)
	if err != nil {
		return nil, fmt.Errorf("could not build the SEC company-ticker request")
	}
	userAgent, err := secUserAgent()
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	client := c.svc.http
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SEC company-ticker file could not be fetched: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SEC company-ticker file returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, secMaxBody))
	if err != nil {
		return nil, fmt.Errorf("SEC company-ticker file could not be read")
	}

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
