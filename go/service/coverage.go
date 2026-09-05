// This file builds the coverage-list capabilities: the eight
// list*Tickers methods, the static list_filing_types and
// list_interest_rate_banks catalogs, and the shared catalog-fetch helper
// they all sit on top of. None of these map to a real Financial Datasets
// MCP tool name (go/mcpserver/tool_schemas.json), so none are registered
// in tools.go's toolHandlers table; each is reachable only through its
// exported Service method in capabilities.go, for the REST layer to wire
// up directly.
//
// --- Capabilities this port's brief explicitly does NOT build, and why ---
//
// As-reported statements: no XBRL/as-reported source in our allowlist, only normalized data.
// IPOs: now implemented via getIPOCalendar (ipocalendar.go), backed by stockanalysis's
// /get_ipo_calendar route rather than the allowlist's pre-IPO private-raise data.
// Ownership state (beneficial/activist/insider/institutional-investor lists): now implemented -
// see beneficialownership.go, insiderownership.go, institutionalinvestors.go. A transport bug
// that blocked the reachable US 13D/13G/insider/institutional-holder SECForm4 routes was fixed;
// those feeds run months stale (see docs/compatibility.md), so every record they produce carries
// its own filing/transaction date rather than being described as current.
// Market-wide price snapshot: it would need a fan-out across thousands of tickers per request.
// CIK-keyed routes: the companies-list catalog carries no cik field; CIKs are instead derived
// per ticker from the SEC EDGAR URLs get_filings already returns (see ownershipshared.go's
// resolveIssuerCIK). Sector lists: the companies-list catalog carries no sector field either,
// but getKPISectors (kpisectors.go) now answers per-ticker sector lookups via finviz instead.
package service

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

// coverageDefaultLimit/coverageMaxLimit bound the list*Tickers `limit`
// argument. The full catalog is ~3,227 tickers (measured live), so a
// default of 1000 already covers most callers in one page, and a max of
// 5000 comfortably covers the whole universe while still rejecting a
// pathological request.
const (
	coverageDefaultLimit = 1000
	coverageMaxLimit     = 5000
)

// coverageLimit validates the shared `limit` argument every list*Tickers
// method takes, before any paid call.
func coverageLimit(args map[string]any) (int, error) {
	limitRaw, err := argIntDefault(args, "limit", coverageDefaultLimit)
	if err != nil {
		return 0, err
	}
	return validateLimit(limitRaw, coverageMaxLimit)
}

// catalogTickerUniverse fetches the DefiLlama US companies-list catalog
// (cached for 24h - see cache.go) and returns its ticker universe: unique,
// upper-cased, sorted ascending. Every list*Tickers method shares this one
// call, so two coverage lists requested inside the same TTL window issue
// exactly one Monid run between them - and, per the measured facts this
// port's brief was built against, that one call has neither a cik nor a
// sector field to offer, only ticker/companyName/country/countryName.
func (c *callCtx) catalogTickerUniverse() ([]string, error) {
	run, err := c.run(defillama, catalogEndpoint, nil, map[string]any{"country": "US"})
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(run.Output, &value); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "provider payload is not valid JSON"}
	}
	records, err := extractGenericRecords(value, []string{"companies", "results", "data"})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(records))
	tickers := make([]string, 0, len(records))
	for _, record := range records {
		symbol := firstStringGeneric(record, "ticker", "symbol")
		if symbol == nil {
			continue
		}
		country := firstStringGeneric(record, "country", "countryCode", "country_code")
		if country != nil && !strings.EqualFold(*country, "US") {
			continue
		}
		upper := strings.ToUpper(strings.TrimSpace(*symbol))
		if upper == "" || seen[upper] {
			continue
		}
		seen[upper] = true
		tickers = append(tickers, upper)
	}
	sort.Strings(tickers)
	return tickers, nil
}

// catalogListResponse shapes one coverage-list response:
// {"resource": resource, "total": <full matched universe size>,
// "tickers": <sorted ascending, capped to limit>}. total is always the
// full universe size, independent of limit, so a caller can tell "there
// are more" from one response without an extra round trip; tickers stays
// sorted ascending across calls so a REST layer can page through it with
// a plain offset.
func catalogListResponse(resource string, tickers []string, limit int) *orderedJSONObject {
	total := len(tickers)
	page := tickers
	if limit < total {
		page = tickers[:limit]
	}
	pageAny := make([]any, len(page))
	for i, t := range page {
		pageAny[i] = t
	}
	out := newOrderedJSONObject()
	out.set("resource", resource)
	out.set("total", total)
	out.set("tickers", pageAny)
	return out
}

// coverageTickers is the shared body of every list*Tickers method: validate
// limit, fetch the shared catalog, shape the response under resource.
func (c *callCtx) coverageTickers(resource string, args map[string]any) (Result, error) {
	limit, err := coverageLimit(args)
	if err != nil {
		return Result{}, err
	}
	tickers, err := c.catalogTickerUniverse()
	if err != nil {
		return Result{}, err
	}
	return Result{Value: catalogListResponse(resource, tickers, limit)}, nil
}

// --- the eight coverage lists ---
//
// All eight draw from the same DefiLlama companies-list catalog (see
// catalogTickerUniverse): the catalog carries no per-dataset dimension
// (no cik, no sector, nothing distinguishing "tickers with earnings data"
// from "tickers with price data"), so honestly, every one of these lists
// is the same ticker universe under a different name. Splitting them into
// eight methods (rather than one generic list_tickers(resource string))
// keeps each one a stable, independently callable capability for the REST
// layer, matching the eight FD-style tickers endpoints this brief asks for.

func (c *callCtx) listCompanyFactsTickers(args map[string]any) (Result, error) {
	return c.coverageTickers("company_facts", args)
}

func (c *callCtx) listEarningsTickers(args map[string]any) (Result, error) {
	return c.coverageTickers("earnings", args)
}

func (c *callCtx) listFilingsTickers(args map[string]any) (Result, error) {
	return c.coverageTickers("filings", args)
}

func (c *callCtx) listMetricsSnapshotTickers(args map[string]any) (Result, error) {
	return c.coverageTickers("financial_metrics_snapshot", args)
}

func (c *callCtx) listPricesTickers(args map[string]any) (Result, error) {
	return c.coverageTickers("prices", args)
}

func (c *callCtx) listPriceSnapshotTickers(args map[string]any) (Result, error) {
	return c.coverageTickers("price_snapshot", args)
}

func (c *callCtx) listInstitutionalHoldingsTickers(args map[string]any) (Result, error) {
	return c.coverageTickers("institutional_holdings", args)
}

func (c *callCtx) listKPITickers(args map[string]any) (Result, error) {
	return c.coverageTickers("kpi", args)
}

// --- list_filing_types (static enum, no paid call) ---

// filingTypeOrder is validFilingTypeEnum's keys (validate.go - the same
// set get_filings/get_filing_items validate filing_type against), in
// Financial Datasets' own documented enum order, so this list can never
// silently drift from what this server actually accepts.
// filingTypeOrder is the published catalog: every form type
// validFilingTypeEnum accepts, sorted, sourced from SEC EDGAR's own
// quarterly form indexes (filingtypes_gen.go). It is derived from
// edgarFormTypes rather than hand-typed, so what this route advertises
// and what get_filings actually accepts cannot drift apart.
var filingTypeOrder = func() []string {
	out := make([]string, len(edgarFormTypes))
	copy(out, edgarFormTypes)
	sort.Strings(out)
	return out
}()

func (c *callCtx) listFilingTypes(args map[string]any) (Result, error) {
	types := make([]any, len(filingTypeOrder))
	for i, t := range filingTypeOrder {
		types[i] = t
	}
	out := newOrderedJSONObject()
	out.set("filing_types", types)
	return Result{Value: out}, nil
}

// --- list_interest_rate_banks (static list, no paid call) ---

// listInterestRateBanks answers with the central banks getInterestRates
// actually scrapes, derived from bankSpecs (interestrates.go) rather than
// hand-typed a second time, so this list can never drift from what
// get_interest_rates actually reads.
//
// Shape follows Financial Datasets' live response, verified 2026-09-04:
// a flat array of bank codes under "banks", sorted, with resource
// "interest_rates" (not "interest_rate_banks"). An earlier version of
// this file answered objects of {bank, name}, which no Financial Datasets
// client would parse. The human-readable name is still reachable: every
// get_interest_rates row carries its own name alongside the rate.
func (c *callCtx) listInterestRateBanks(args map[string]any) (Result, error) {
	codes := make([]string, len(bankSpecs))
	for i, spec := range bankSpecs {
		codes[i] = spec.Bank
	}
	sort.Strings(codes)
	banks := make([]any, len(codes))
	for i, code := range codes {
		banks[i] = code
	}
	out := newOrderedJSONObject()
	out.set("resource", "interest_rates")
	out.set("banks", banks)
	return Result{Value: out}, nil
}
