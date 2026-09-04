"""Financial Datasets-compatible MCP server surface.

Registers the exact Financial Datasets tool names, input parameters, and
descriptions captured live from the upstream MCP. List tools return the
bare record array; object tools return the bare object; failures answer
with the Financial Datasets ErrorResponse shape at zero cost.
"""
from __future__ import annotations

import os
from collections.abc import Awaitable, Callable
from pathlib import Path
from typing import Literal

from mcp.server.fastmcp import FastMCP

from monid_finance_mcp.client import MonidClient
from monid_finance_mcp.compat import fd_error
from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.service import FinanceService

NOT_IMPLEMENTED = (
    "This Financial Datasets tool is not implemented by the Monid-backed "
    "server yet; the call was free and no data was fabricated."
)

PERIOD = Literal["annual", "quarterly", "ttm"]
FILING_TYPE = Literal["10-K", "10-Q", "8-K"]
INTERVAL = Literal["day", "week", "month", "year"]

_DESCRIPTIONS: dict[str, str] = {
    'get_beneficial_owners': "Lists beneficial owners (holders of more than 5% of a company's shares, from SEC Schedules 13D/13G) with their filer CIK and reporting-person name. Optionally filter by case-insensitive name prefix. The response's `total` is the full match count; when it exceeds the returned page, narrow the search with `name`. Use this to discover the filer_cik to pass to get_beneficial_ownership.",  # noqa: E501
    'get_beneficial_ownership': "Retrieves beneficial-ownership stakes (holders of more than 5% of a class of shares) from SEC Schedules 13D and 13G. Schedule 13D stakes are ACTIVIST (intent to influence control: proxy fights, board seats, pushing for a sale); Schedule 13G stakes are passive (large asset managers). Query by ticker (who owns this company) OR by filer_cik (what stakes does this owner hold) — exactly one is required. Use type=activist to isolate activist stakes. By default each stake's CURRENT state is returned; set history=true for the full amendment chain. Each row is one reporting person with voting/dispositive powers, percent_of_class, and (for 13D) the stated purpose_of_transaction. Coverage begins January 2025.",  # noqa: E501
    'get_company_facts': 'Get comprehensive company facts data for a stock ticker or CIK from Financial Datasets. Returns real-time information including market cap, number of employees, sector/industry classification, exchange listing, company location, website URL, SIC codes, weighted average shares, and historical events like ticker changes.',  # noqa: E501
    'get_earnings': 'Retrieves earnings data from SEC filings. Returns a flat list of `EarningsRecord` entries — same shape in either mode.\n  • COMPANY EARNINGS — pass a `ticker` to get the most recent SEC filings (8-K / 10-Q / 10-K / 20-F) for that company. The same `report_period` may appear in two consecutive entries when an 8-K announcement and the matching 10-Q have both been filed; the 8-K comes first because it filed earlier. Sorted `(report_period DESC, filing_date ASC)`.\n  • EARNINGS FEED — omit `ticker` to get the real-time feed of the most recently filed earnings across all covered companies, sorted by `filing_date` descending and deduped by `(ticker, report_period)`.\nEach entry exposes `ticker`, `report_period`, `fiscal_period`, `currency`, `source_type`, `filing_date`, `filing_url`, `accession_number`, plus `quarterly` and/or `annual` financial blocks (revenue, EPS, surprise vs estimates, etc.).',  # noqa: E501
    'get_filing_items': "Retrieves specific sections (items) from a company's SEC filings (10-K, 10-Q, or 8-K). Useful for extracting detailed information such as 'Business', 'Risk Factors', or 'Financial Statements and Supplementary Data'.",  # noqa: E501
    'list_filing_item_types': 'Provides a list of all available item names that can be extracted from 10-K, 10-Q, and 8-K reports, grouped by filing type.',  # noqa: E501
    'get_filings': 'Get SEC filings data for a stock ticker or CIK. Returns a list of filings, including the accession number, filing type, report date, and URLs to the filing documents.',  # noqa: E501
    'get_financial_metrics': 'Retrieves historical financial metrics for a company, such as P/E ratio, revenue per share, and enterprise value, over a specified period. Useful for trend analysis and historical performance evaluation.',  # noqa: E501
    'get_financial_metrics_snapshot': "Fetches a snapshot of the most current financial metrics for a company, including key indicators like market capitalization, P/E ratio, and dividend yield. Useful for a quick overview of a company's financial health.",  # noqa: E501
    'get_balance_sheet': "Retrieves a company's balance sheet, which provides a snapshot of its assets, liabilities, and shareholders' equity at a specific point in time. Essential for assessing a company's financial position.",  # noqa: E501
    'get_income_statement': "Fetches a company's income statement, detailing its revenues, expenses, and net income over a reporting period. Useful for evaluating a company's profitability and operational efficiency.",  # noqa: E501
    'get_cash_flow_statement': "Provides a company's cash flow statement, showing how cash is generated and used across operating, investing, and financing activities. Key for understanding a company's liquidity and solvency.",  # noqa: E501
    'get_index_fund': "Get an ETF or index fund's holdings and each position's weight (percent of net assets) for a fund ticker (e.g. SPY), sourced from SEC fund-holdings filings. Returns the fund's latest filing by default, or the composition as of a past date. Response includes a fund header (as-of period, total net assets, coverage counts) and the holdings sorted by weight descending.",  # noqa: E501
    'get_insider_ownership': "Retrieves insider ownership statements for a company: what officers, directors, and 10% owners actually HOLD (common shares, options, RSUs), sourced from SEC Form 3 (an insider's initial statement of ownership) and Form 5 (the annual statement). Complements get_insider_trades, which covers the buys and sells in between: trades are the events, ownership statements are the state. Positions are returned as reported per filing, newest first. To find available insider names for a ticker, use: https://api.financialdatasets.ai/insider-ownership/names/?ticker={ticker}",  # noqa: E501
    'get_insider_trades': 'Retrieves insider trading transactions for a company, including purchases, sales, and other transactions by company officers, directors, and major shareholders. Useful for tracking insider sentiment and ownership changes. To find available insider names for a ticker, use: https://api.financialdatasets.ai/insider-trades/names/?ticker={ticker}',  # noqa: E501
    'get_institutional_investors': 'Lists institutional investors (SEC 13F filers) with their CIK and most recent reported name. Optionally filter by case-insensitive name prefix. Use this to discover the filer_cik to pass to get_institutional_holdings.',  # noqa: E501
    'get_institutional_holdings': 'Retrieves SEC 13F institutional holdings sourced directly from EDGAR. Query by filer_cik (what positions a filer holds) OR by ticker (which institutional filers hold this security) — exactly one is required. When no report_period filter is supplied, returns the latest available quarter for the filer or ticker. Ticker-mode rows include filer_cik and filer_name per position; filer-mode rows omit those (the filer is already implied by the query).',  # noqa: E501
    'get_interest_rates': "Retrieves the latest policy interest rates from major central banks (e.g., FED, ECB, BOE, BOJ). Returns a snapshot of each bank's current rate — no parameters required.",  # noqa: E501
    'get_kpi_guidance': 'Retrieves forward-looking KPI guidance items issued by company management in earnings releases and SEC filings. Filter by ticker, period (quarterly/annual), metric name, and date range. Available to Pro and Enterprise customers only.',  # noqa: E501
    'get_kpi_metrics': 'Retrieves historical KPI taxonomy metrics extracted from SEC filings for a company (e.g., subscriber counts, ARPU, units sold). Filter by ticker, period (quarterly/annual), metric name, and date range. Available to Pro and Enterprise customers only.',  # noqa: E501
    'get_kpi_non_gaap': 'Retrieves non-GAAP KPIs reported by a company (e.g., Adjusted EBITDA, Free Cash Flow as defined by the company). Filter by ticker, period (quarterly/annual), metric name, and date range. Available to Pro and Enterprise customers only.',  # noqa: E501
    'get_news': 'Fetches recent news articles for a specific company or the broad market. Pass a ticker for company-specific news. Call with no arguments for the latest market-moving news, covering AI, macro, rates, earnings, geopolitics, war, energy, crypto, IPOs, housing, and other market-wide topics. Also useful when trying to explain broad price moves — omit the ticker to check for market-wide catalysts.',  # noqa: E501
    'get_segmented_financials': 'Retrieves segment breakdowns from all three financial statement types (income statement, balance sheet, cash flow) in a single call. Returns revenue, operating income, and depreciation by product/segment; assets, goodwill, and long-lived assets by segment; and capital expenditure by segment. Essential for sum-of-the-parts valuation and segment-level analysis.',  # noqa: E501
    'get_stock_price': 'Fetches the most recent price snapshot for a specific stock, including the latest price, trading volume, and other open, high, low, and close price data.',  # noqa: E501
    'get_stock_prices': 'Retrieves historical price data for a stock over a specified date range, including open, high, low, close prices, and volume.',  # noqa: E501
    'screen_stocks': 'Screens and filters stocks by financial metrics, valuation ratios, and company attributes. Combine multiple filter conditions to find stocks matching your investment criteria (e.g., revenue > $1B and P/E ratio < 20). Use list_stock_screener_filters first to see all available fields and operators.',  # noqa: E501
    'list_stock_screener_filters': 'Retrieves all available filter fields and operators for the stock screener, grouped by category (income statement, balance sheet, cash flow statement, financial metrics, and company attributes like sector and industry). Use this to discover which fields and operators you can use with the screen_stocks tool.',  # noqa: E501
}

async def _collect(
    fetch: Callable[..., Awaitable[JsonObject]],
    *,
    key: str,
    limit: int | None,
    kwargs: dict[str, object],
) -> JsonValue:
    """Aggregate bare records across service pages.

    The MCP surface exposes no cursor parameter; service methods paginate at
    a page size smaller than an allowed limit. Follow the opaque cursor in
    each next_page_url until the limit is reached or the pages end, then
    return the bare record array. With limit=None, fetch every page.
    """
    collected: list[JsonObject] = []
    while limit is None or len(collected) < limit:
        response = await fetch(**kwargs)
        if "error" in response:
            return response
        records = response.get(key)
        if not isinstance(records, list):
            return fd_error("upstream_error", f"{key} missing from service response.")
        for record in records:
            if isinstance(record, dict):
                collected.append(record)
        next_url = response.get("next_page_url")
        if not isinstance(next_url, str) or "cursor=" not in next_url:
            break
        kwargs = {**kwargs, "cursor": next_url.split("cursor=", 1)[1]}
    return collected if limit is None else collected[:limit]


def _unwrap(response: JsonObject, key: str) -> JsonValue:
    """Return the bare object a snapshot tool exposes, unless it is an error."""
    if "error" in response:
        return response
    value = response.get(key)
    return value if isinstance(value, dict) else response


def create_server(service: FinanceService | None = None) -> FastMCP:
    finance = service or FinanceService(
        MonidClient(
            cli=os.getenv("MONID_CLI", "monid"),
            run_timeout_seconds=_run_timeout(),
            allowlist_path=_allowlist_path(),
        )
    )
    server = FastMCP("Monid Finance MCP")


    @server.tool(name="get_company_facts", description=_DESCRIPTIONS["get_company_facts"])
    async def get_company_facts(
        ticker: str | None = None, cik: str | None = None
    ) -> JsonValue:
        """Get company facts for a stock ticker or CIK."""
        response = await finance.get_company_facts(ticker=ticker, cik=cik)
        return _unwrap(response, "company_facts")

    @server.tool(name="get_income_statement", description=_DESCRIPTIONS["get_income_statement"])
    async def get_income_statement(
        ticker: str,
        period: PERIOD = "annual",
        limit: int = 4,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        report_period: str | None = None,
        as_reported: bool = False,
    ) -> JsonValue:
        """Get income statements for a company."""
        if as_reported:
            return fd_error(
                "bad_request",
                "as_reported is not supported by the Monid-backed server; "
                "as_reported=True cannot be answered honestly.",
            )
        return await _collect(
            finance.get_income_statement,
            key="income_statements",
            limit=limit,
            kwargs={
                "ticker": ticker,
                "period": period,
                "limit": limit,
                "report_period_gte": report_period_gte,
                "report_period_lte": report_period_lte,
                "report_period_gt": report_period_gt,
                "report_period_lt": report_period_lt,
                "report_period": report_period,
            },
        )

    @server.tool(name="get_balance_sheet", description=_DESCRIPTIONS["get_balance_sheet"])
    async def get_balance_sheet(
        ticker: str,
        period: PERIOD = "annual",
        limit: int = 4,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        report_period: str | None = None,
        as_reported: bool = False,
    ) -> JsonValue:
        """Get balance sheets for a company."""
        if as_reported:
            return fd_error(
                "bad_request",
                "as_reported is not supported by the Monid-backed server; "
                "as_reported=True cannot be answered honestly.",
            )
        return await _collect(
            finance.get_balance_sheet,
            key="balance_sheets",
            limit=limit,
            kwargs={
                "ticker": ticker,
                "period": period,
                "limit": limit,
                "report_period_gte": report_period_gte,
                "report_period_lte": report_period_lte,
                "report_period_gt": report_period_gt,
                "report_period_lt": report_period_lt,
                "report_period": report_period,
            },
        )

    @server.tool(
        name="get_cash_flow_statement", description=_DESCRIPTIONS["get_cash_flow_statement"]
    )
    async def get_cash_flow_statement(
        ticker: str,
        period: PERIOD = "annual",
        limit: int = 4,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        report_period: str | None = None,
        as_reported: bool = False,
    ) -> JsonValue:
        """Get cash flow statements for a company."""
        if as_reported:
            return fd_error(
                "bad_request",
                "as_reported is not supported by the Monid-backed server; "
                "as_reported=True cannot be answered honestly.",
            )
        return await _collect(
            finance.get_cash_flow_statement,
            key="cash_flow_statements",
            limit=limit,
            kwargs={
                "ticker": ticker,
                "period": period,
                "limit": limit,
                "report_period_gte": report_period_gte,
                "report_period_lte": report_period_lte,
                "report_period_gt": report_period_gt,
                "report_period_lt": report_period_lt,
                "report_period": report_period,
            },
        )

    @server.tool(
        name="get_financial_metrics_snapshot",
        description=_DESCRIPTIONS["get_financial_metrics_snapshot"],
    )
    async def get_financial_metrics_snapshot(
        ticker: str,
    ) -> JsonValue:
        """Get a snapshot of the latest financial metrics for a company."""
        response = await finance.get_financial_metrics_snapshot(ticker=ticker)
        return _unwrap(response, "snapshot")

    @server.tool(name="get_filings", description=_DESCRIPTIONS["get_filings"])
    async def get_filings(
        ticker: str | None = None,
        cik: str | None = None,
        limit: int = 10,
        filing_type: list[str] | None = None,
    ) -> JsonValue:
        """Get recent SEC filings for a company."""
        return await _collect(
            finance.get_filings,
            key="filings",
            limit=limit,
            kwargs={
                "ticker": ticker,
                "cik": cik,
                "filing_type": filing_type,
                "limit": limit,
            },
        )

    @server.tool(name="get_stock_prices", description=_DESCRIPTIONS["get_stock_prices"])
    async def get_stock_prices(
        ticker: str,
        interval: INTERVAL = "day",
        interval_multiplier: int = 1,
        start_date: str | None = None,
        end_date: str | None = None,
    ) -> JsonValue:
        """Get historical stock prices over a date range."""
        if interval_multiplier != 1:
            return fd_error(
                "bad_request",
                "only interval_multiplier=1 is supported by the Monid-backed server.",
            )
        return await _collect(
            finance.get_stock_prices,
            key="prices",
            limit=None,
            kwargs={
                "ticker": ticker,
                "interval": interval,
                "start_date": start_date,
                "end_date": end_date,
            },
        )

    @server.tool(name="get_stock_price", description=_DESCRIPTIONS["get_stock_price"])
    async def get_stock_price(ticker: str) -> JsonValue:
        """Get the latest stock price for a company."""
        response = await finance.get_stock_price(ticker)
        return _unwrap(response, "snapshot")

    @server.tool(name="get_news", description=_DESCRIPTIONS["get_news"])
    async def get_news(
        ticker: str | None = None, limit: int = 10
    ) -> JsonValue:
        """Get recent news for a company or the broad market."""
        return await _collect(
            finance.get_news,
            key="news",
            limit=limit,
            kwargs={"ticker": ticker, "limit": limit},
        )

    @server.tool(name="get_filing_items", description=_DESCRIPTIONS["get_filing_items"])
    async def get_filing_items(
        ticker: str,
        filing_type: FILING_TYPE,
        year: int | None = None,
        quarter: int | None = None,
        accession_number: str | None = None,
        item: str | None = None,
    ) -> JsonValue:
        """Get specific items from a company's SEC filings."""
        return await finance.get_filing_items(
            ticker=ticker,
            filing_type=filing_type,
            year=year,
            quarter=quarter,
            item=item,
            accession_number=accession_number,
        )

    @server.tool(name="list_filing_item_types", description=_DESCRIPTIONS["list_filing_item_types"])
    async def list_filing_item_types() -> JsonValue:
        """List the filing item names available per filing type."""
        return await finance.list_filing_item_types(None)

    @server.tool(name="get_beneficial_owners", description=_DESCRIPTIONS["get_beneficial_owners"])
    async def get_beneficial_owners(
        name: str | None = None, limit: int = 10
    ) -> JsonValue:
        """List beneficial owners of a company."""
        del name, limit
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(
        name="get_beneficial_ownership", description=_DESCRIPTIONS["get_beneficial_ownership"]
    )
    async def get_beneficial_ownership(
        ticker: str | None = None,
        filer_cik: str | None = None,
        type: str | None = None,
        history: bool = False,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Get beneficial ownership stakes from SEC Schedules 13D/13G."""
        del (ticker, filer_cik, type, history, filing_date,
             filing_date_gte, filing_date_lte, limit)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_earnings", description=_DESCRIPTIONS["get_earnings"])
    async def get_earnings(
        ticker: str | None = None,
    ) -> JsonValue:
        """Get earnings data from SEC filings."""
        return await _collect(
            finance.get_earnings,
            key="earnings",
            limit=5,
            kwargs={"ticker": ticker, "limit": 5},
        )

    @server.tool(name="get_financial_metrics", description=_DESCRIPTIONS["get_financial_metrics"])
    async def get_financial_metrics(
        ticker: str,
        period: PERIOD = "annual",
        limit: int = 4,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        report_period: str | None = None,
    ) -> JsonValue:
        """Get historical financial metrics for a company."""
        return await _collect(
            finance.get_financial_metrics,
            key="financial_metrics",
            limit=limit,
            kwargs={
                "ticker": ticker,
                "period": period,
                "limit": limit,
                "report_period_gte": report_period_gte,
                "report_period_lte": report_period_lte,
                "report_period_gt": report_period_gt,
                "report_period_lt": report_period_lt,
                "report_period": report_period,
            },
        )

    @server.tool(name="get_index_fund", description=_DESCRIPTIONS["get_index_fund"])
    async def get_index_fund(
        ticker: str,
        as_of: str | None = None,
        asset_class: str | None = None,
        limit: int = 10,
        offset: int = 0,
    ) -> JsonValue:
        """Get an index fund's holdings and weights."""
        del (ticker, as_of, asset_class, limit, offset)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_insider_ownership", description=_DESCRIPTIONS["get_insider_ownership"])
    async def get_insider_ownership(
        ticker: str,
        name: str | None = None,
        form_type: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        filing_date_gt: str | None = None,
        filing_date_lt: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Get insider ownership statements for a company."""
        del (ticker, name, form_type, filing_date, filing_date_gte,
             filing_date_lte, filing_date_gt, filing_date_lt, limit)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_insider_trades", description=_DESCRIPTIONS["get_insider_trades"])
    async def get_insider_trades(
        ticker: str,
        name: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Get insider trading transactions for a company."""
        return await _collect(
            finance.get_insider_trades,
            key="insider_trades",
            limit=limit,
            kwargs={
                "ticker": ticker,
                "name": name,
                "filing_date": filing_date,
                "filing_date_gte": filing_date_gte,
                "filing_date_lte": filing_date_lte,
                "limit": limit,
            },
        )

    @server.tool(
        name="get_institutional_investors", description=_DESCRIPTIONS["get_institutional_investors"]
    )
    async def get_institutional_investors(
        name: str | None = None,
    ) -> JsonValue:
        """List institutional investors."""
        del name
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(
        name="get_institutional_holdings", description=_DESCRIPTIONS["get_institutional_holdings"]
    )
    async def get_institutional_holdings(
        filer_cik: str | None = None,
        ticker: str | None = None,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Get institutional holdings from SEC 13F filings."""
        del (filer_cik, ticker, report_period, report_period_gte,
             report_period_lte, limit)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_interest_rates", description=_DESCRIPTIONS["get_interest_rates"])
    async def get_interest_rates() -> JsonValue:
        """Get policy interest rates from major central banks."""
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_kpi_guidance", description=_DESCRIPTIONS["get_kpi_guidance"])
    async def get_kpi_guidance(
        ticker: str,
        period: Literal["annual", "quarterly"] = "annual",
        metric_name: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Get forward-looking KPI guidance issued by management."""
        del (ticker, period, metric_name, report_period_gte,
             report_period_lte, limit)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_kpi_metrics", description=_DESCRIPTIONS["get_kpi_metrics"])
    async def get_kpi_metrics(
        ticker: str,
        period: Literal["annual", "quarterly"] = "annual",
        metric_name: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Get historical KPI taxonomy metrics."""
        del (ticker, period, metric_name, report_period_gte,
             report_period_lte, limit)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="get_kpi_non_gaap", description=_DESCRIPTIONS["get_kpi_non_gaap"])
    async def get_kpi_non_gaap(
        ticker: str,
        period: Literal["annual", "quarterly"] = "annual",
        metric_name: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Get non-GAAP KPIs reported by a company."""
        del (ticker, period, metric_name, report_period_gte,
             report_period_lte, limit)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(
        name="get_segmented_financials", description=_DESCRIPTIONS["get_segmented_financials"]
    )
    async def get_segmented_financials(
        ticker: str,
        period: PERIOD = "annual",
        limit: int = 10,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        report_period: str | None = None,
    ) -> JsonValue:
        """Get segment breakdowns for a company."""
        del (ticker, period, limit, report_period_gte, report_period_lte,
             report_period_gt, report_period_lt, report_period)
        return {"error": "not_implemented", "message": NOT_IMPLEMENTED}

    @server.tool(name="screen_stocks", description=_DESCRIPTIONS["screen_stocks"])
    async def screen_stocks(
        filters: list[JsonObject],
        currency: str | None = None,
        limit: int = 10,
    ) -> JsonValue:
        """Screen stocks by company attributes and financial metrics."""
        if currency is not None and currency != "USD":
            return fd_error(
                "bad_request",
                "only USD currency is supported by the Monid-backed server.",
            )
        return await _collect(
            finance.screen_stocks,
            key="search_results",
            limit=limit,
            kwargs={"filters": filters, "limit": limit},
        )

    @server.tool(
        name="list_stock_screener_filters", description=_DESCRIPTIONS["list_stock_screener_filters"]
    )
    async def list_stock_screener_filters() -> JsonValue:
        """List stock screener filters and operators."""
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