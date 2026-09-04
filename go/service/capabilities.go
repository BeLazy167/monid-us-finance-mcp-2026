// This file exposes every new capability from this port's brief as an
// exported Service method, for the REST layer to call directly. None of
// them map to a real Financial Datasets MCP tool name
// (go/mcpserver/tool_schemas.json), so none are registered in tools.go's
// toolHandlers table (that table is Service.Call's dispatch, and
// Service.Call/the MCP server only ever look up real MCP tool names
// there); each capability's tool name below exists only as a ledger
// receipt label; it is never a valid Service.Call argument.
package service

import "context"

// ListCompanyFactsTickers returns the tickers list_company_facts_tickers'
// coverage-list request can serve (see coverage.go).
func (s *Service) ListCompanyFactsTickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_company_facts_tickers").listCompanyFactsTickers(args)
}

// ListEarningsTickers returns the tickers list_earnings_tickers' coverage-
// list request can serve.
func (s *Service) ListEarningsTickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_earnings_tickers").listEarningsTickers(args)
}

// ListFilingsTickers returns the tickers list_filings_tickers' coverage-
// list request can serve.
func (s *Service) ListFilingsTickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_filings_tickers").listFilingsTickers(args)
}

// ListMetricsSnapshotTickers returns the tickers
// list_metrics_snapshot_tickers' coverage-list request can serve.
func (s *Service) ListMetricsSnapshotTickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_metrics_snapshot_tickers").listMetricsSnapshotTickers(args)
}

// ListPricesTickers returns the tickers list_prices_tickers' coverage-list
// request can serve.
func (s *Service) ListPricesTickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_prices_tickers").listPricesTickers(args)
}

// ListPriceSnapshotTickers returns the tickers list_price_snapshot_tickers'
// coverage-list request can serve.
func (s *Service) ListPriceSnapshotTickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_price_snapshot_tickers").listPriceSnapshotTickers(args)
}

// ListInstitutionalHoldingsTickers returns the tickers
// list_institutional_holdings_tickers' coverage-list request can serve.
func (s *Service) ListInstitutionalHoldingsTickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_institutional_holdings_tickers").listInstitutionalHoldingsTickers(args)
}

// ListKPITickers returns the tickers list_kpi_tickers' coverage-list
// request can serve.
func (s *Service) ListKPITickers(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_kpi_tickers").listKPITickers(args)
}

// ListFilingTypes returns the static filing_type enum this server
// validates get_filings/get_filing_items filing_type arguments against.
// No paid call.
func (s *Service) ListFilingTypes(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_filing_types").listFilingTypes(args)
}

// ListFilingItemTypes exposes list_filing_item_types to a REST route as an
// exported Service method. Note this capability is also already reachable
// through the normal MCP dispatch path (Service.Call(ctx, apiKey,
// "list_filing_item_types", args)): it maps to a real MCP tool name and is
// already registered in tools.go's toolHandlers. This wrapper exists only
// so the REST layer can call it the same typed way as every other new
// capability in this file, without special-casing the one that happens to
// already have an MCP tool name.
func (s *Service) ListFilingItemTypes(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_filing_item_types").listFilingItemTypes(args)
}

// ListInterestRateBanks returns the central banks get_interest_rates
// actually scrapes (bankSpecs, interestrates.go). No paid call.
func (s *Service) ListInterestRateBanks(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_interest_rate_banks").listInterestRateBanks(args)
}

// GetAllFinancials composes income statement, balance sheet, and cash flow
// statement for one ticker into {"financials": {...}} (see
// allfinancials.go).
func (s *Service) GetAllFinancials(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "get_all_financials").getAllFinancials(args)
}

// SearchLineItems searches an arbitrary set of statement field names
// across an arbitrary (capped) set of tickers (see searchlineitems.go).
func (s *Service) SearchLineItems(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "search_line_items").searchLineItems(args)
}

// ListFilingsCIKs returns every CIK in SEC's published company-ticker
// file, in file order (see secciks.go). Free: SEC publishes this file, so
// the call is not routed through a paid provider.
func (s *Service) ListFilingsCIKs(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_filings_ciks").listFilingsCIKs(args)
}

// ListCompanyFactsCIKs returns that same universe deduplicated and
// zero-padded. Free, for the same reason.
func (s *Service) ListCompanyFactsCIKs(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "list_company_facts_ciks").listCompanyFactsCIKs(args)
}

// GetKPISectors batch-resolves the sector (and, when present, a
// performance figure) for up to 100 tickers via finviz's
// /get_ticker_sectors_with_performance route (kpisectors.go).
func (s *Service) GetKPISectors(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "get_kpi_sectors").getKPISectors(args)
}

// GetIPOs returns one issuer's S-1 and S-1/A registration statements from
// the same filings feed get_filings reads (see ipos.go). Distinct from
// GetIPOCalendar, which is a forward calendar of upcoming offerings.
func (s *Service) GetIPOs(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "get_ipos").getIPOs(args)
}

// GetIPOCalendar returns the flattened this-week/next-week/later IPO
// calendar via stockanalysis's /get_ipo_calendar route (ipocalendar.go).
func (s *Service) GetIPOCalendar(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "get_ipo_calendar").getIPOCalendar(args)
}

// GetMarketSnapshot returns nasdaq's current most-active-by-share-volume
// snapshot via /get_market_movers (marketsnapshot.go). Unlike every other
// capability in this file, its data is current as of the snapshot's own
// data_as_of/last_trade_timestamp fields, not historical.
func (s *Service) GetMarketSnapshot(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "get_market_snapshot").getMarketSnapshot(args)
}

// GetAsReported serves all four as-reported statement routes from SEC
// EDGAR's rendered statement files (see asreported.go). The "statement"
// argument selects the variant, so one capability backs four routes that
// differ only in which statement they read and which envelope key they
// answer with.
func (s *Service) GetAsReported(ctx context.Context, apiKey string, args map[string]any) (Result, error) {
	return s.newCallCtx(ctx, apiKey, "get_as_reported").getAsReported(args)
}
