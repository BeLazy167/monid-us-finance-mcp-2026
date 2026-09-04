"""Financial Datasets-compatible MCP server surface.

Registers the exact Financial Datasets tool names and input parameters.
Tools without an honest Monid-backed implementation answer with the
Financial Datasets ErrorResponse shape at zero cost.
"""
from __future__ import annotations

import os
from pathlib import Path
from typing import Literal

from mcp.server.fastmcp import FastMCP

from monid_finance_mcp.client import MonidClient
from monid_finance_mcp.models import JsonObject
from monid_finance_mcp.service import FinanceService

NOT_IMPLEMENTED = (
    "This Financial Datasets tool is not implemented by the Monid-backed "
    "server yet; the call was free and no data was fabricated."
)

def create_server(service: FinanceService | None = None) -> FastMCP:
    finance = service or FinanceService(
        MonidClient(
            cli=os.getenv("MONID_CLI", "monid"),
            run_timeout_seconds=_run_timeout(),
            allowlist_path=_allowlist_path(),
        )
    )
    server = FastMCP("Monid Finance MCP")

    @server.tool(name="get_company_facts")
    async def get_company_facts(
        ticker: str | None = None, cik: str | None = None
    ) -> JsonObject:
        """Get company details such as name, sector, industry, and exchange."""
        return await finance.get_company_facts(ticker=ticker, cik=cik)

    @server.tool(name="get_income_statement")
    async def get_income_statement(
        ticker: str | None = None,
        period: Literal["annual", "quarterly", "ttm"] = "annual",
        limit: int = 4,
        cik: str | None = None,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        filing_date_gt: str | None = None,
        filing_date_lt: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get income statements for a company with optional pagination."""
        return await finance.get_income_statement(
            ticker=ticker,
            period=period,
            limit=limit,
            cik=cik,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
            report_period_gt=report_period_gt,
            report_period_lt=report_period_lt,
            filing_date=filing_date,
            filing_date_gte=filing_date_gte,
            filing_date_lte=filing_date_lte,
            filing_date_gt=filing_date_gt,
            filing_date_lt=filing_date_lt,
            cursor=cursor,
        )

    @server.tool(name="get_balance_sheet")
    async def get_balance_sheet(
        ticker: str | None = None,
        period: Literal["annual", "quarterly", "ttm"] = "annual",
        limit: int = 4,
        cik: str | None = None,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        filing_date_gt: str | None = None,
        filing_date_lt: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get balance sheets for a company with optional pagination."""
        return await finance.get_balance_sheet(
            ticker=ticker,
            period=period,
            limit=limit,
            cik=cik,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
            report_period_gt=report_period_gt,
            report_period_lt=report_period_lt,
            filing_date=filing_date,
            filing_date_gte=filing_date_gte,
            filing_date_lte=filing_date_lte,
            filing_date_gt=filing_date_gt,
            filing_date_lt=filing_date_lt,
            cursor=cursor,
        )

    @server.tool(name="get_cash_flow_statement")
    async def get_cash_flow_statement(
        ticker: str | None = None,
        period: Literal["annual", "quarterly", "ttm"] = "annual",
        limit: int = 4,
        cik: str | None = None,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        filing_date_gt: str | None = None,
        filing_date_lt: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get cash flow statements for a company with optional pagination."""
        return await finance.get_cash_flow_statement(
            ticker=ticker,
            period=period,
            limit=limit,
            cik=cik,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
            report_period_gt=report_period_gt,
            report_period_lt=report_period_lt,
            filing_date=filing_date,
            filing_date_gte=filing_date_gte,
            filing_date_lte=filing_date_lte,
            filing_date_gt=filing_date_gt,
            filing_date_lt=filing_date_lt,
            cursor=cursor,
        )

    @server.tool(name="get_financial_metrics_snapshot")
    async def get_financial_metrics_snapshot(
        ticker: str | None = None, cik: str | None = None
    ) -> JsonObject:
        """Get a snapshot of the latest financial metrics for a company."""
        return await finance.get_financial_metrics_snapshot(ticker=ticker, cik=cik)

    @server.tool(name="get_filings")
    async def get_filings(
        ticker: str | None = None,
        cik: str | None = None,
        filing_type: list[str] | None = None,
        limit: int = 10,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get the most recent SEC filings for a company."""
        return await finance.get_filings(
            ticker=ticker,
            cik=cik,
            filing_type=filing_type,
            limit=limit,
            cursor=cursor,
        )

    @server.tool(name="get_stock_prices")
    async def get_stock_prices(
        ticker: str,
        interval: Literal["day", "week", "month", "year"] = "day",
        start_date: str | None = None,
        end_date: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get stock price data for a company over a date range."""
        return await finance.get_stock_prices(
            ticker=ticker,
            interval=interval,
            start_date=start_date,
            end_date=end_date,
            cursor=cursor,
        )

    @server.tool(name="get_stock_price")
    async def get_stock_price(ticker: str) -> JsonObject:
        """Get the latest stock price for a company."""
        return await finance.get_stock_price(ticker)

    @server.tool(name="get_news")
    async def get_news(
        ticker: str | None = None, limit: int = 5, cursor: str | None = None
    ) -> JsonObject:
        """Get the latest news for a company."""
        return await finance.get_news(ticker=ticker, limit=limit, cursor=cursor)

    @server.tool(name="get_filing_items")
    async def get_filing_items(
        ticker: str,
        filing_type: Literal["10-K", "10-Q", "8-K"],
        year: int,
        quarter: int | None = None,
        item: str | None = None,
        accession_number: str | None = None,
        include_exhibits: bool = False,
    ) -> JsonObject:
        """Get individual items from a filing, such as Item 1 from a 10-K."""
        return await finance.get_filing_items(
            ticker=ticker,
            filing_type=filing_type,
            year=year,
            quarter=quarter,
            item=item,
            accession_number=accession_number,
            include_exhibits=include_exhibits,
        )

    @server.tool(name="list_filing_item_types")
    async def list_filing_item_types(
        filing_type: Literal["10-K", "10-Q", "8-K"] | None = None,
    ) -> JsonObject:
        """List all filing item types for a given filing type (10-K, 10-Q, 8-K)."""
        return await finance.list_filing_item_types(filing_type)

    @server.tool(name="get_beneficial_owners")
    async def get_beneficial_owners(
        ticker: str | None = None, cursor: str | None = None
    ) -> JsonObject:
        """Get 5%+ beneficial owners of a company. Not implemented yet."""
        del ticker, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_beneficial_ownership")
    async def get_beneficial_ownership(
        ticker: str | None = None,
        filer_cik: str | None = None,
        type: str | None = None,
        history: bool = False,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get 5%+ beneficial-ownership stakes from 13D/13G. Not implemented yet."""
        del ticker, filer_cik, type, history, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_earnings")
    async def get_earnings(
        ticker: str | None = None, limit: int = 1, cursor: str | None = None
    ) -> JsonObject:
        """Get earnings data composed from SEC 10-K and 10-Q filings."""
        return await finance.get_earnings(ticker=ticker, limit=limit, cursor=cursor)

    @server.tool(name="get_financial_metrics")
    async def get_financial_metrics(
        ticker: str | None = None,
        period: Literal["annual", "quarterly", "ttm"] = "annual",
        limit: int = 4,
        cik: str | None = None,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        filing_date_gt: str | None = None,
        filing_date_lt: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get historical financial metrics and ratios for a company."""
        return await finance.get_financial_metrics(
            ticker=ticker,
            period=period,
            limit=limit,
            cik=cik,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
            report_period_gt=report_period_gt,
            report_period_lt=report_period_lt,
            filing_date=filing_date,
            filing_date_gte=filing_date_gte,
            filing_date_lte=filing_date_lte,
            filing_date_gt=filing_date_gt,
            filing_date_lt=filing_date_lt,
            cursor=cursor,
        )

    @server.tool(name="get_index_fund")
    async def get_index_fund(
        ticker: str, fund: str | None = None, as_of: str | None = None
    ) -> JsonObject:
        """Get index fund composition and holdings. Not implemented yet."""
        del ticker, fund, as_of
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_insider_ownership")
    async def get_insider_ownership(
        ticker: str | None = None, limit: int = 10, cursor: str | None = None
    ) -> JsonObject:
        """Get insider ownership of a company. Not implemented yet."""
        del ticker, limit, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_insider_trades")
    async def get_insider_trades(
        ticker: str | None = None,
        limit: int = 10,
        name: str | None = None,
        transaction_type: str | None = None,
        form_type: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        filing_date_gt: str | None = None,
        filing_date_lt: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get insider trading transactions for a company."""
        return await finance.get_insider_trades(
            ticker=ticker,
            limit=limit,
            name=name,
            transaction_type=transaction_type,
            form_type=form_type,
            filing_date=filing_date,
            filing_date_gte=filing_date_gte,
            filing_date_lte=filing_date_lte,
            filing_date_gt=filing_date_gt,
            filing_date_lt=filing_date_lt,
            cursor=cursor,
        )

    @server.tool(name="get_institutional_investors")
    async def get_institutional_investors(
        ticker: str | None = None, limit: int = 10, cursor: str | None = None
    ) -> JsonObject:
        """Get institutional investors of a company. Not implemented yet."""
        del ticker, limit, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_institutional_holdings")
    async def get_institutional_holdings(
        ticker: str | None = None,
        filer_cik: str | None = None,
        limit: int = 10,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get institutional holdings for a company. Not implemented yet."""
        del ticker, filer_cik, limit, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_interest_rates")
    async def get_interest_rates(
        start_date: str | None = None,
        end_date: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get historical interest rates. Not implemented yet."""
        del start_date, end_date, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_kpi_guidance")
    async def get_kpi_guidance(
        ticker: str, kpi: str | None = None, cursor: str | None = None
    ) -> JsonObject:
        """Get company KPI guidance. Not implemented yet."""
        del ticker, kpi, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_kpi_metrics")
    async def get_kpi_metrics(
        ticker: str, kpi: str | None = None, cursor: str | None = None
    ) -> JsonObject:
        """Get company KPI metrics. Not implemented yet."""
        del ticker, kpi, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_kpi_non_gaap")
    async def get_kpi_non_gaap(
        ticker: str, kpi: str | None = None, cursor: str | None = None
    ) -> JsonObject:
        """Get company non-GAAP KPIs. Not implemented yet."""
        del ticker, kpi, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_segmented_financials")
    async def get_segmented_financials(
        ticker: str | None = None,
        period: Literal["annual", "quarterly", "ttm"] = "annual",
        limit: int = 10,
        cursor: str | None = None,
    ) -> JsonObject:
        """Get segmented financials for a company. Not implemented yet."""
        del ticker, period, limit, cursor
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="screen_stocks")
    async def screen_stocks(
        filters: list[JsonObject] | None = None,
        limit: int = 10,
        cursor: str | None = None,
    ) -> JsonObject:
        """Screen stocks by company attributes (exchange, market cap)."""
        return await finance.screen_stocks(filters=filters, limit=limit, cursor=cursor)

    @server.tool(name="list_stock_screener_filters")
    async def list_stock_screener_filters() -> JsonObject:
        """List available stock screener filters and operators."""
        return await finance.list_stock_screener_filters()

    _ = (
        get_company_facts,
        get_income_statement,
        get_balance_sheet,
        get_cash_flow_statement,
        get_financial_metrics_snapshot,
        get_filings,
        get_stock_prices,
        get_stock_price,
        get_news,
        get_filing_items,
        list_filing_item_types,
        get_beneficial_owners,
        get_beneficial_ownership,
        get_earnings,
        get_financial_metrics,
        get_index_fund,
        get_insider_ownership,
        get_insider_trades,
        get_institutional_investors,
        get_institutional_holdings,
        get_interest_rates,
        get_kpi_guidance,
        get_kpi_metrics,
        get_kpi_non_gaap,
        get_segmented_financials,
        screen_stocks,
        list_stock_screener_filters,
    )
    return server


def _allowlist_path() -> Path | None:
    raw = os.getenv("MONID_ALLOWLIST_PATH")
    if raw:
        return Path(raw)
    default = Path(__file__).resolve().parents[2] / "docs" / "monid_finance_discovery.json"
    return default if default.exists() else None


def _run_timeout() -> float:
    raw = os.getenv("MONID_RUN_TIMEOUT_SECONDS", "90")
    try:
        value = float(raw)
    except ValueError as error:
        raise ValueError("MONID_RUN_TIMEOUT_SECONDS must be numeric.") from error
    return value


mcp = create_server()


def main() -> None:
    mcp.run(transport="stdio")
