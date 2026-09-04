from __future__ import annotations

import os
from typing import Literal

from mcp.server.fastmcp import FastMCP

from monid_finance_mcp.client import MonidClient
from monid_finance_mcp.models import EnvelopeDict
from monid_finance_mcp.service import FinanceService


def create_server(service: FinanceService | None = None) -> FastMCP:
    finance = service or FinanceService(
        MonidClient(
            cli=os.getenv("MONID_CLI", "monid"),
            run_timeout_seconds=_run_timeout(),
        )
    )
    server = FastMCP("Monid Finance MCP")

    @server.tool(name="get_company_facts")
    async def get_company_facts(ticker: str) -> EnvelopeDict:
        """Get US company identity and an indicative live market summary."""
        return await finance.get_company_facts(ticker)

    @server.tool(name="get_income_statement")
    async def get_income_statement(
        ticker: str,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
    ) -> EnvelopeDict:
        """Get annual, quarterly, or provider-reported TTM income statements."""
        return await finance.get_income_statement(
            ticker,
            period=period,
            limit=limit,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
        )

    @server.tool(name="get_balance_sheet")
    async def get_balance_sheet(
        ticker: str,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
    ) -> EnvelopeDict:
        """Get annual, quarterly, or provider-reported TTM balance sheets."""
        return await finance.get_balance_sheet(
            ticker,
            period=period,
            limit=limit,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
        )

    @server.tool(name="get_cash_flow_statement")
    async def get_cash_flow_statement(
        ticker: str,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
    ) -> EnvelopeDict:
        """Get annual, quarterly, or provider-reported TTM cash flow statements."""
        return await finance.get_cash_flow_statement(
            ticker,
            period=period,
            limit=limit,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
        )

    @server.tool(name="get_financial_metrics_snapshot")
    async def get_financial_metrics_snapshot(ticker: str) -> EnvelopeDict:
        """Get an indicative DefiLlama market and financial metrics snapshot."""
        return await finance.get_financial_metrics_snapshot(ticker)

    @server.tool(name="get_filings")
    async def get_filings(
        ticker: str,
        filing_type: list[str] | None = None,
        limit: int = 10,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
    ) -> EnvelopeDict:
        """Get a filtered US SEC filing index with direct document URLs."""
        return await finance.get_filings(
            ticker,
            filing_type=filing_type,
            limit=limit,
            filing_date_gte=filing_date_gte,
            filing_date_lte=filing_date_lte,
        )

    @server.tool(name="get_filing_items")
    async def get_filing_items(
        ticker: str,
        filing_type: Literal["10-K", "10-Q", "8-K"],
        year: int,
        quarter: int | None = None,
        item: str | None = None,
        accession_number: str | None = None,
        include_exhibits: bool = False,
    ) -> EnvelopeDict:
        """Extract one or all supported sections from a selected US SEC filing."""
        return await finance.get_filing_items(
            ticker,
            filing_type,
            year,
            quarter=quarter,
            item=item,
            accession_number=accession_number,
            include_exhibits=include_exhibits,
        )

    @server.tool(name="list_filing_item_types")
    async def list_filing_item_types(filing_type: str | None = None) -> EnvelopeDict:
        """List the static, SEC-sourced item catalog without making a paid call."""
        return await finance.list_filing_item_types(filing_type)

    @server.tool(name="get_stock_prices")
    async def get_stock_prices(
        ticker: str,
        start_date: str,
        end_date: str,
        interval: str = "day",
    ) -> EnvelopeDict:
        """Get delayed or EOD OHLCV and aggregate it to a requested interval."""
        return await finance.get_stock_prices(
            ticker,
            start_date=start_date,
            end_date=end_date,
            interval=interval,
        )

    @server.tool(name="get_stock_price")
    async def get_stock_price(ticker: str) -> EnvelopeDict:
        """Get an indicative latest stock-price summary."""
        return await finance.get_stock_price(ticker)

    @server.tool(name="get_news")
    async def get_news(
        ticker: str | None = None,
        limit: int = 5,
        start_date: str | None = None,
        end_date: str | None = None,
    ) -> EnvelopeDict:
        """Get entity-matched company news. This slice requires a ticker."""
        return await finance.get_news(
            ticker,
            limit=limit,
            start_date=start_date,
            end_date=end_date,
        )

    _ = (
        get_company_facts,
        get_income_statement,
        get_balance_sheet,
        get_cash_flow_statement,
        get_financial_metrics_snapshot,
        get_filings,
        get_filing_items,
        list_filing_item_types,
        get_stock_prices,
        get_stock_price,
        get_news,
    )
    return server


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
