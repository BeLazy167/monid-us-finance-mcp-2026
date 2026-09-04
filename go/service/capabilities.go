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
