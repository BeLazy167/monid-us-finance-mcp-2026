"""Financial Datasets API contract service layer.

Every tool returns either a Financial Datasets response object or the
Financial Datasets ErrorResponse shape ``{"error", "message"}``.
Provenance, cost, and warnings live in the committed receipts ledger,
never inside responses.
"""
from __future__ import annotations

import json
from collections.abc import Awaitable, Callable
from datetime import date
from functools import wraps
from typing import TypeVar

from monid_finance_mcp.cache import RunCache, cache_ttl_for
from monid_finance_mcp.client import (
    MonidClientProtocol,
    MonidError,
    MonidProviderHTTPError,
    MonidRun,
    MonidTimeoutError,
)
from monid_finance_mcp.compat import (
    PRICES_PAGE_SIZE,
    CursorError,
    decode_cursor,
    fd_error,
    paginate,
)
from monid_finance_mcp.fd import (
    FilingIdentity,
    balance_sheet_record,
    cash_flow_record,
    company_facts_response,
    filing_item_record,
    filing_items_response,
    filing_record,
    income_statement_record,
    index_fund_response,
    insider_trade_record,
    institutional_holdings_response,
    interest_rate_record,
    interest_rates_response,
    list_response,
    metric_snapshot_record,
    news_record,
    price_record,
    price_snapshot_response,
    prices_response,
    screener_filters_response,
    screener_search_result,
)
from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.earnings import (
    normalize_earnings,
    parse_earnings_calendar,
)
from monid_finance_mcp.providers.us.filing_items import (
    derive_accession,
    normalize_accession,
    validate_sec_url,
)
from monid_finance_mcp.providers.us.financial_metrics import (
    MetricsPeriod,
    normalize_financial_metrics,
)
from monid_finance_mcp.providers.us.index_fund import (
    SCRAPE_ENDPOINT,
    index_fund_search_request,
    parse_as_of,
    parse_holdings,
    pick_holdings_candidates,
    validate_asset_class,
)
from monid_finance_mcp.providers.us.index_fund import (
    SEARCH_ENDPOINT as INDEX_FUND_SEARCH_ENDPOINT,
)
from monid_finance_mcp.providers.us.index_fund import (
    parse_scrape_markdown as parse_fund_scrape,
)
from monid_finance_mcp.providers.us.index_fund import (
    scrape_query as fund_scrape_query,
)
from monid_finance_mcp.providers.us.insider_trades import normalize_insider_trades
from monid_finance_mcp.providers.us.institutional_holdings import (
    normalize_institutional_holdings,
)
from monid_finance_mcp.providers.us.interest_rates import (
    BANK_SPECS,
    parse_policy_rate,
)
from monid_finance_mcp.providers.us.interest_rates import (
    parse_scrape_markdown as parse_bank_scrape,
)
from monid_finance_mcp.providers.us.interest_rates import (
    scrape_query as bank_scrape_query,
)
from monid_finance_mcp.providers.us.kpi import (
    KPI_GUIDANCE_INSTRUCTIONS,
    KPI_METRICS_INSTRUCTIONS,
    KPI_NONGAAP_INSTRUCTIONS,
    kpi_guidance_extract_schema,
    kpi_metrics_extract_schema,
    kpi_nongaap_extract_schema,
    normalize_kpi_guidance,
    normalize_kpi_metrics,
    normalize_kpi_nongaap,
    validate_kpi_period,
)
from monid_finance_mcp.providers.us.normalize import (
    InputError,
    SchemaDriftError,
    find_company,
    normalize_filings,
    normalize_news,
    normalize_prices,
    normalize_stock_price,
    normalize_summary,
    validate_date,
    validate_date_range,
    validate_filing_types,
    validate_interval,
    validate_limit,
    validate_period,
    validate_ticker,
)
from monid_finance_mcp.providers.us.segmented_financials import (
    SEGMENT_INSTRUCTIONS,
    normalize_segmented_financials,
    segment_extract_schema,
)
from monid_finance_mcp.providers.us.statements import (
    PeriodRow,
    StatementSeries,
    derive_ttm_rows,
    filter_rows,
    fiscal_period_label,
    fiscal_year_end_month,
    parse_statement_series,
)
from monid_finance_mcp.providers.us.stock_screener import (
    normalize_screener,
    validate_screener_request,
)
from monid_finance_mcp.providers.us.web_extract import (
    EXTRACT_ENDPOINT,
    extract_request,
    parse_extract_output,
)
from monid_finance_mcp.receipts import ReceiptsLedger

DEFILLAMA = "defillama"
CATALOG_ENDPOINT = "/equities/v1/companies-list"
SUMMARY_ENDPOINT = "/equities/v1/summary"
STATEMENTS_ENDPOINT = "/equities/v1/statements"
FILINGS_ENDPOINT = "/equities/v1/filings"
OHLCV_ENDPOINT = "/equities/v1/ohlcv"
NEWS_PROVIDER = "context.dev"
NEWS_ENDPOINT = "/news/search"
SECFORM4 = "secform4"
INSIDER_ENDPOINT = "/search"
INSTITUTIONAL_ENDPOINT = "/get_institution_holders"
NASDAQ = "nasdaq"
SCREENER_ENDPOINT = "/get_stock_screener"
SCREENER_MAX_ROWS = 25
EARNINGS_CALENDAR_ENDPOINT = "/get_earnings_calendar"

STATEMENT_RESPONSE_KEYS = {
    "income": ("income_statements", "income-statements"),
    "balance": ("balance_sheets", "balance-sheets"),
    "cash": ("cash_flow_statements", "cash-flow-statements"),
}
ANNUAL_FORMS = ("10-K", "20-F")
QUARTERLY_FORMS = ("10-Q", "6-K")

_TTM_FLOW_INCOME = frozenset(
    {
        "Revenue",
        "Cost of Revenue",
        "Gross Profit",
        "Operating Expenses",
        "Operating Income",
        "EBIT",
        "Income Tax",
        "Net Income",
        "Net Income to Common",
        "EPS (Basic)",
        "EPS (Diluted)",
        "Non-Operating Items|Non-Operating Interest Expense",
    }
)
_TTM_MEAN_INCOME = frozenset(
    {"Shares Outstanding (Basic)", "Shares Outstanding (Diluted)"}
)
_TTM_FLOW_CASH = frozenset(
    {
        "Net Income",
        "Depreciation and Amortization",
        "Cash Flow from Operating Activities",
        "Cash Flow from Investing Activities",
        "Cash Flow from Financing Activities",
        "Net Cash Flow",
        "Free Cash Flow",
        "Cash Flow from Investing Activities|Capital Expenditure",
        "Cash Flow from Financing Activities|Common Dividends",
    }
)

_T = TypeVar("_T")


class UpstreamError(Exception):
    """A Monid upstream call failed; carry the FD error code to respond with."""

    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


def _fd_errors(tool: Callable[..., Awaitable[JsonObject]]) -> Callable[..., Awaitable[JsonObject]]:
    """Convert tool failures into Financial Datasets ErrorResponse objects."""

    @wraps(tool)
    async def wrapper(*args: object, **kwargs: object) -> JsonObject:
        try:
            return await tool(*args, **kwargs)  # type: ignore[arg-types]
        except InputError as error:
            return fd_error("bad_request", str(error))
        except CursorError as error:
            return fd_error("invalid_cursor", str(error))
        except SchemaDriftError as error:
            return fd_error("schema_drift", str(error))
        except UpstreamError as error:
            return fd_error(error.code, str(error))

    return wrapper


class FinanceService:
    def __init__(
        self,
        client: MonidClientProtocol,
        ledger: ReceiptsLedger | None = None,
        cache: RunCache | None = None,
    ) -> None:
        self._client = client
        self._ledger = ledger if ledger is not None else ReceiptsLedger()
        self._cache = cache if cache is not None else RunCache()

    async def _call(
        self,
        tool: str,
        provider: str,
        endpoint: str,
        *,
        body: JsonObject | None = None,
        query_params: JsonObject | None = None,
    ) -> MonidRun:
        """Run one Monid call, record a receipt, and map failures to FD errors.

        Repeat calls with identical inputs are served from the TTL run cache:
        no new Monid run, no wallet spend, no ledger row.
        """
        cache_key = _cache_key(provider, endpoint, body, query_params)
        cached = self._cache.get(cache_key)
        if cached is not None:
            return cached
        try:
            run = await self._client.run(
                provider, endpoint, body=body, query_params=query_params
            )
        except MonidTimeoutError as error:
            self._ledger.record_failure(
                tool=tool, provider=provider, endpoint=endpoint, error=error,
                body=body, query_params=query_params,
            )
            raise UpstreamError("timeout", str(error)) from error
        except MonidProviderHTTPError as error:
            self._ledger.record_failure(
                tool=tool, provider=provider, endpoint=endpoint, error=error,
                body=body, query_params=query_params,
            )
            raise UpstreamError("upstream_error", str(error)) from error
        except MonidError as error:
            self._ledger.record_failure(
                tool=tool, provider=provider, endpoint=endpoint, error=error,
                body=body, query_params=query_params,
            )
            raise UpstreamError("upstream_error", str(error)) from error
        self._ledger.record_success(tool=tool, run=run, body=body, query_params=query_params)
        self._cache.put(cache_key, run, ttl_seconds=cache_ttl_for(endpoint))
        return run

    @_fd_errors
    async def get_company_facts(
        self, ticker: str | None = None, cik: str | None = None
    ) -> JsonObject:
        if ticker is None:
            raise InputError("ticker is required (cik lookup is not supported)")
        if cik is not None:
            raise InputError("cik lookup is not supported; pass ticker instead")
        symbol = validate_ticker(ticker)
        catalog = await self._call(
            "get_company_facts", DEFILLAMA, CATALOG_ENDPOINT
        )
        company = find_company(catalog.output, symbol)
        if company is None:
            return fd_error("not_found", f"No US company record exists for ticker {symbol}.")
        name = company.get("name")
        return company_facts_response(
            ticker=symbol,
            name=name if isinstance(name, str) else None,
            sector=None,
            industry=None,
            exchange=None,
        )

    async def get_income_statement(
        self,
        ticker: str | None = None,
        period: str = "annual",
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
        return await self._statement_response(
            "get_income_statement",
            "income",
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

    async def get_balance_sheet(
        self,
        ticker: str | None = None,
        period: str = "annual",
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
        return await self._statement_response(
            "get_balance_sheet",
            "balance",
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

    async def get_cash_flow_statement(
        self,
        ticker: str | None = None,
        period: str = "annual",
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
        return await self._statement_response(
            "get_cash_flow_statement",
            "cash",
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

    @_fd_errors
    async def _statement_response(
        self,
        tool: str,
        statement: str,
        *,
        ticker: str | None,
        period: str,
        limit: int,
        cik: str | None,
        report_period: str | None,
        report_period_gte: str | None,
        report_period_lte: str | None,
        report_period_gt: str | None,
        report_period_lt: str | None,
        filing_date: str | None,
        filing_date_gte: str | None,
        filing_date_lte: str | None,
        filing_date_gt: str | None,
        filing_date_lt: str | None,
        cursor: str | None,
    ) -> JsonObject:
        if ticker is None:
            raise InputError("ticker is required (cik lookup is not supported)")
        if cik is not None:
            raise InputError("cik lookup is not supported; pass ticker instead")
        symbol = validate_ticker(ticker)
        normalized_period = validate_period(period)
        bounded_limit = validate_limit(limit, maximum=100)
        report = _date_filters(
            report_period,
            report_period_gte,
            report_period_lte,
            report_period_gt,
            report_period_lt,
            prefix="report_period",
        )
        filing = _date_filters(
            filing_date,
            filing_date_gte,
            filing_date_lte,
            filing_date_gt,
            filing_date_lt,
            prefix="filing_date",
        )
        offset = decode_cursor(cursor)[0] if cursor is not None else 0

        statements_run = await self._call(
            tool, DEFILLAMA, STATEMENTS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        series = parse_statement_series(statements_run.output, statement)
        rows = self._statement_rows(series, normalized_period, statement)
        end_month = fiscal_year_end_month(series)
        _ = end_month
        identity_map = await self._filing_identity_map(
            tool, symbol, annual=normalized_period != "quarterly"
        )
        if identity_map is None and any(value is not None for value in filing.values()):
            raise UpstreamError(
                "upstream_error",
                "Filing identity join failed; filing_date filters cannot be applied.",
            )
        rows = _apply_filing_filters(rows, identity_map, filing)
        rows = filter_rows(rows, **report)
        rows = sorted(rows, key=lambda row: row.report_period, reverse=True)[:bounded_limit]

        records = [
            _statement_record(
                statement=statement,
                ticker=symbol,
                period=normalized_period,
                row=row,
                identity=identity_map.get(row.report_period) if identity_map is not None else None,
                fiscal_period=(
                    fiscal_period_label(row, end_month, is_annual=normalized_period == "annual")
                    if normalized_period != "ttm"
                    else None
                ),
            )
            for row in rows
        ]
        response_key, path = STATEMENT_RESPONSE_KEYS[statement]
        page = paginate(records, offset=offset, path=f"/financials/{path}")
        return list_response(response_key, page.records, page.next_url)

    def _statement_rows(
        self, series: StatementSeries, period: str, statement: str
    ) -> list[PeriodRow]:
        if period == "annual":
            return list(series.annual)
        if period == "quarterly":
            return list(series.quarterly)
        flow_labels: frozenset[str] = frozenset()
        mean_labels: frozenset[str] = frozenset()
        if statement == "income":
            flow_labels = _TTM_FLOW_INCOME
            mean_labels = _TTM_MEAN_INCOME
        elif statement == "cash":
            flow_labels = _TTM_FLOW_CASH
        return derive_ttm_rows(series.quarterly, flow_labels=flow_labels, mean_labels=mean_labels)

    async def _filing_identity_map(
        self, tool: str, ticker: str, *, annual: bool
    ) -> dict[date, FilingIdentity] | None:
        """Join DefiLlama filings for statement identity; None means join failed."""
        try:
            filings_run = await self._call(
                tool, DEFILLAMA, FILINGS_ENDPOINT,
                query_params={"ticker": ticker, "country": "US"},
            )
        except UpstreamError:
            return None
        records = normalize_filings(
            filings_run.output, filing_types=None, limit=10_000,
            filing_date_gte=None, filing_date_lte=None,
        )
        forms = ANNUAL_FORMS if annual else QUARTERLY_FORMS
        best: dict[date, tuple[date, FilingIdentity]] = {}
        for record in records:
            form = _opt_str(record.get("form"))
            if form is None or form.upper() not in forms:
                continue
            report_day = _opt_date(record.get("report_date"))
            filing_day = _opt_date(record.get("filing_date"))
            url = _opt_str(record.get("primary_document_url"))
            if report_day is None or filing_day is None or url is None:
                continue
            accession = derive_accession(url)
            identity = FilingIdentity(
                accession_number=accession,
                form_type=form.upper(),
                filing_url=url,
                filing_date=filing_day,
            )
            current = best.get(report_day)
            if current is None or filing_day > current[0]:
                best[report_day] = (filing_day, identity)
        return {day: identity for day, (_, identity) in best.items()}

    @_fd_errors
    async def get_financial_metrics_snapshot(
        self, ticker: str | None = None, cik: str | None = None
    ) -> JsonObject:
        if ticker is None:
            raise InputError("ticker is required (cik lookup is not supported)")
        if cik is not None:
            raise InputError("cik lookup is not supported; pass ticker instead")
        symbol = validate_ticker(ticker)
        run = await self._call(
            "get_financial_metrics_snapshot", DEFILLAMA, SUMMARY_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        summary = normalize_summary(run.output, symbol)
        gross_margin = _ratio(summary.get("gross_profit_ttm"), summary.get("revenue_ttm"))
        net_margin = _ratio(summary.get("net_income_ttm"), summary.get("revenue_ttm"))
        operating_margin = _as_ratio(_num(summary.get("operating_profit_margin_ttm")))
        return metric_snapshot_record(
            ticker=symbol,
            market_cap=_num(summary.get("market_cap")),
            enterprise_value=_num(summary.get("enterprise_value")),
            trailing_pe=_num(summary.get("price_to_earnings_ratio")),
            price_to_book=_num(summary.get("price_to_book")),
            price_to_revenue=_num(summary.get("price_to_revenue")),
            ev_to_ebitda=_num(summary.get("enterprise_value_to_ebitda")),
            gross_margin_ttm=gross_margin,
            operating_margin_ttm=operating_margin,
            net_margin_ttm=net_margin,
        )

    @_fd_errors
    async def get_filings(
        self,
        ticker: str | None = None,
        cik: str | None = None,
        filing_type: list[str] | None = None,
        limit: int = 10,
        cursor: str | None = None,
    ) -> JsonObject:
        if cik is not None:
            raise InputError("cik lookup is not supported; pass ticker instead")
        if ticker is None:
            raise InputError("ticker or cik is required")
        symbol = validate_ticker(ticker)
        forms = validate_filing_types(filing_type)
        bounded_limit = validate_limit(limit, maximum=1000)
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        run = await self._call(
            "get_filings", DEFILLAMA, FILINGS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        records = normalize_filings(
            run.output,
            filing_types=forms,
            limit=bounded_limit,
            filing_date_gte=None,
            filing_date_lte=None,
        )
        filings = [
            filing_record(
                ticker=symbol,
                report_date=_opt_str(record.get("report_date")),
                filing_date=_opt_str(record.get("filing_date")),
                form=_opt_str(record.get("form")),
                url=_opt_str(record.get("primary_document_url")),
                accession_number=derive_accession(
                    _opt_str(record.get("primary_document_url")) or ""
                ),
            )
            for record in records
        ]
        page = paginate(filings, offset=offset, path="/filings")
        return list_response("filings", page.records, page.next_url)

    @_fd_errors
    async def get_stock_prices(
        self,
        ticker: str,
        interval: str = "day",
        start_date: str | None = None,
        end_date: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        symbol = validate_ticker(ticker)
        normalized_interval = validate_interval(interval)
        start, end = validate_date_range(start_date, end_date, "start_date", "end_date")
        if start is None or end is None:
            raise InputError("start_date and end_date are required")
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        run = await self._call(
            "get_stock_prices", DEFILLAMA, OHLCV_ENDPOINT,
            query_params={"ticker": symbol, "country": "US", "timeframe": "MAX"},
        )
        bars = normalize_prices(
            run.output, start_date=start, end_date=end, interval=normalized_interval
        )
        records = [
            price_record(
                day=_bar_time(bar, normalized_interval),
                open_=_num(bar["open"]) or 0.0,
                high=_num(bar["high"]) or 0.0,
                low=_num(bar["low"]) or 0.0,
                close=_num(bar["close"]) or 0.0,
                volume=_num(bar["volume"]) or 0.0,
            )
            for bar in bars
        ]
        page = paginate(
            records, offset=offset, path="/prices", page_size=PRICES_PAGE_SIZE
        )
        return prices_response(symbol, page.records, page.next_url)

    @_fd_errors
    async def get_stock_price(self, ticker: str) -> JsonObject:
        symbol = validate_ticker(ticker)
        run = await self._call(
            "get_stock_price", DEFILLAMA, SUMMARY_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        summary = normalize_stock_price(run.output, symbol)
        return price_snapshot_response(
            ticker=symbol,
            price=_num(summary.get("price")) or 0.0,
            day_change=_num(summary.get("day_change")),
            day_change_percent=_num(summary.get("day_change_percent")),
            time=_opt_str(summary.get("as_of")),
        )

    @_fd_errors
    async def get_news(
        self,
        ticker: str | None = None,
        limit: int = 5,
        cursor: str | None = None,
    ) -> JsonObject:
        if ticker is None:
            raise InputError("market-wide news is not routed; pass ticker")
        symbol = validate_ticker(ticker)
        bounded_limit = validate_limit(limit, maximum=10)
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        news_body: JsonObject = {
            "searchBy": {
                "type": "entity",
                "entity": {"type": "ticker", "ticker": symbol},
            },
            "sortBy": {"type": "newest"},
            "limit": bounded_limit,
        }
        run = await self._call("get_news", NEWS_PROVIDER, NEWS_ENDPOINT, body=news_body)
        articles = normalize_news(run.output, limit=bounded_limit, start_date=None, end_date=None)
        records = [
            news_record(
                ticker=symbol,
                title=_opt_str(article.get("title")),
                source=_opt_str(article.get("source")),
                date_=_opt_str(article.get("published_at")),
                url=_opt_str(article.get("url")),
            )
            for article in articles
        ]
        page = paginate(records, offset=offset, path="/news")
        return list_response("news", page.records, page.next_url)

    @_fd_errors
    async def get_earnings(
        self,
        ticker: str | None = None,
        limit: int = 1,
        cursor: str | None = None,
    ) -> JsonObject:
        bounded_limit = validate_limit(limit, maximum=40)
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        if ticker is None:
            records = await self._earnings_feed(bounded_limit)
        else:
            symbol = validate_ticker(ticker)
            records = await self._earnings_for_ticker(
                "get_earnings", symbol, limit=bounded_limit
            )
        page = paginate(records, offset=offset, path="/earnings")
        return list_response("earnings", page.records, page.next_url)

    async def _earnings_feed(self, limit: int) -> list[JsonObject]:
        """Compose the market-wide earnings feed from the Nasdaq calendar."""
        calendar_run = await self._call(
            "get_earnings", NASDAQ, EARNINGS_CALENDAR_ENDPOINT,
            query_params={"limit": limit},
        )
        reporters = parse_earnings_calendar(calendar_run.output, limit=limit)
        records: list[JsonObject] = []
        for reporter in reporters:
            entry_symbol = validate_ticker(reporter.ticker)
            try:
                composed = await self._earnings_for_ticker(
                    "get_earnings", entry_symbol, limit=1
                )
            except (UpstreamError, SchemaDriftError):
                continue
            records.extend(composed)
        records.sort(key=_earnings_filing_sort_key, reverse=True)
        return records

    async def get_financial_metrics(
        self,
        ticker: str | None = None,
        period: str = "annual",
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
        return await self._financial_metrics_response(
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

    @_fd_errors
    async def _financial_metrics_response(
        self,
        *,
        ticker: str | None,
        period: str,
        limit: int,
        cik: str | None,
        report_period: str | None,
        report_period_gte: str | None,
        report_period_lte: str | None,
        report_period_gt: str | None,
        report_period_lt: str | None,
        filing_date: str | None,
        filing_date_gte: str | None,
        filing_date_lte: str | None,
        filing_date_gt: str | None,
        filing_date_lt: str | None,
        cursor: str | None,
    ) -> JsonObject:
        if ticker is None:
            raise InputError("ticker is required (cik lookup is not supported)")
        if cik is not None:
            raise InputError("cik lookup is not supported; pass ticker instead")
        symbol = validate_ticker(ticker)
        normalized_period = validate_period(period)
        bounded_limit = validate_limit(limit, maximum=100)
        report = _date_filters(
            report_period,
            report_period_gte,
            report_period_lte,
            report_period_gt,
            report_period_lt,
            prefix="report_period",
        )
        filing = _date_filters(
            filing_date,
            filing_date_gte,
            filing_date_lte,
            filing_date_gt,
            filing_date_lt,
            prefix="filing_date",
        )
        offset = decode_cursor(cursor)[0] if cursor is not None else 0

        statements_run = await self._call(
            "get_financial_metrics",
            DEFILLAMA,
            STATEMENTS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        metrics_period: MetricsPeriod = (
            normalized_period if normalized_period in ("annual", "quarterly", "ttm") else "annual"
        )
        data = normalize_financial_metrics(
            statements_run.output,
            ticker=symbol,
            period=metrics_period,
            limit=bounded_limit,
            report_period=report["exact"],
            report_period_gte=report["gte"],
            report_period_lte=report["lte"],
            report_period_gt=report["gt"],
            report_period_lt=report["lt"],
        )
        identity_map = await self._filing_identity_map(
            "get_financial_metrics", symbol, annual=normalized_period != "quarterly"
        )
        if identity_map is None and any(value is not None for value in filing.values()):
            raise UpstreamError(
                "upstream_error",
                "Filing identity join failed; filing_date filters cannot be applied.",
            )
        records: list[JsonObject] = []
        for record in data.records:
            report_day = _opt_date(record.get("report_period"))
            enriched = dict(record)
            if identity_map is not None and report_day is not None:
                identity = identity_map.get(report_day)
                if identity is not None:
                    enriched["accession_number"] = identity.accession_number
                    enriched["form_type"] = identity.form_type
                    enriched["filing_url"] = identity.filing_url
                    if identity.filing_date is not None:
                        enriched["filing_date"] = identity.filing_date.isoformat()
            if any(value is not None for value in filing.values()):
                filing_day_value = enriched.get("filing_date")
                filing_day = _opt_date(filing_day_value) if filing_day_value is not None else None
                if filing_day is None or not _date_matches(
                    filing_day,
                    exact=filing["exact"],
                    gte=filing["gte"],
                    lte=filing["lte"],
                    gt=filing["gt"],
                    lt=filing["lt"],
                ):
                    continue
            if metrics_period == "ttm":
                enriched.pop("accession_number", None)
                enriched.pop("form_type", None)
                enriched.pop("filing_url", None)
                enriched.pop("filing_date", None)
            records.append(_ordered_metrics_record(enriched))
        page = paginate(records, offset=offset, path="/financial-metrics")
        return list_response("financial_metrics", page.records, page.next_url)

    @_fd_errors
    async def get_insider_trades(
        self,
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
        if ticker is None:
            raise InputError("ticker is required")
        if form_type is not None:
            raise InputError(
                "form_type filtering is not supported: the validated SECForm4 route "
                "does not report form types"
            )
        symbol = validate_ticker(ticker)
        bounded_limit = validate_limit(limit, maximum=15)
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        filing = _date_filters(
            filing_date,
            filing_date_gte,
            filing_date_lte,
            filing_date_gt,
            filing_date_lt,
            prefix="filing_date",
        )
        run = await self._call(
            "get_insider_trades",
            SECFORM4,
            INSIDER_ENDPOINT,
            query_params={"query": symbol},
        )
        data = normalize_insider_trades(
            run.output,
            ticker=symbol,
            limit=bounded_limit,
            name=name,
            transaction_type=transaction_type,
            filing_date=filing["exact"],
            filing_date_gte=filing["gte"],
            filing_date_lte=filing["lte"],
        )
        records: list[JsonObject] = []
        for row in data.records:
            records.append(
                insider_trade_record(
                    ticker=symbol,
                    issuer=_opt_str(row.get("company")),
                    name=_opt_str(row.get("insider_relationship")),
                    filing_date=_opt_str(row.get("filing_date")),
                    transaction_date=_opt_str(row.get("transaction_date")),
                    transaction_type=_opt_str(row.get("transaction_type")),
                    transaction_shares=_opt_num(row.get("shares_traded")),
                    transaction_price_per_share=_opt_num(row.get("average_price")),
                    transaction_value=_opt_num(row.get("total_amount")),
                    shares_owned_after_transaction=_opt_num(row.get("shares_owned")),
                )
            )
        page = paginate(records, offset=offset, path="/insider-trades")
        return list_response("insider_trades", page.records, page.next_url)

    @_fd_errors
    async def screen_stocks(
        self,
        filters: list[JsonObject] | None = None,
        limit: int = 10,
        cursor: str | None = None,
    ) -> JsonObject:
        if filters is None:
            raise InputError(
                "filters is required and must include exchange and/or market_cap"
            )
        bounded_limit = validate_limit(limit, maximum=100)
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        request = validate_screener_request(filters, bounded_limit, offset)
        run = await self._call(
            "screen_stocks",
            NASDAQ,
            SCREENER_ENDPOINT,
            query_params=request.query_params,
        )
        data = normalize_screener(run.output)
        records: list[JsonObject] = []
        exchange_value = request.query_params.get("exchange")
        for row in data.records[: bounded_limit]:
            market_cap = row.get("market_cap")
            last_sale = row.get("last_sale")
            net_change = row.get("net_change")
            percent_change = row.get("percent_change")
            records.append(
                screener_search_result(
                    ticker=_opt_str(row.get("ticker")) or "",
                    exchange=_opt_str(exchange_value),
                    market_cap=_metric_string(market_cap),
                    last_sale=_metric_string(last_sale),
                    net_change=_metric_string(net_change),
                    percent_change=_metric_string(percent_change),
                )
            )
        return list_response("search_results", records, None)

    @_fd_errors
    async def list_stock_screener_filters(self) -> JsonObject:
        return screener_filters_response()

    @_fd_errors
    async def get_filing_items(
        self,
        ticker: str,
        filing_type: str,
        year: int | None = None,
        quarter: int | None = None,
        item: str | None = None,
        accession_number: str | None = None,
        include_exhibits: bool = False,
    ) -> JsonObject:
        from monid_finance_mcp.providers.us.filing_items import (
            parse_filing_sections,
            parse_scrape_payload,
            resolve_item,
            select_filing,
            validate_catalog_filing_type,
            validate_filing_item_request,
        )

        if include_exhibits:
            raise InputError(
                "include_exhibits is not supported: the validated route cannot "
                "identify and fetch filing exhibits"
            )
        normalized_accession = normalize_accession(accession_number)
        if year is None:
            symbol = validate_ticker(ticker)
            normalized_type = validate_catalog_filing_type(filing_type)
            if quarter is not None and (isinstance(quarter, bool) or not 1 <= quarter <= 4):
                raise InputError("quarter must be between 1 and 4")
            selected_item = resolve_item(normalized_type, item) if item is not None else None
            filings_run = await self._call(
                "get_filing_items", DEFILLAMA, FILINGS_ENDPOINT,
                query_params={"ticker": symbol, "country": "US"},
            )
            selected_year = _latest_filing_year(
                filings_run.output,
                filing_type=normalized_type,
                quarter=quarter,
                accession_number=normalized_accession,
            )
            selected_quarter = quarter
            if selected_year is None:
                return fd_error(
                    "not_found",
                    f"No {normalized_type} filing matches ticker {symbol}.",
                )
        else:
            symbol, normalized_type, selected_year, selected_quarter, selected_item = (
                validate_filing_item_request(ticker, filing_type, year, quarter, item)
            )
            filings_run = await self._call(
                "get_filing_items", DEFILLAMA, FILINGS_ENDPOINT,
                query_params={"ticker": symbol, "country": "US"},
            )
        selection = select_filing(
            filings_run.output,
            filing_type=normalized_type,
            year=selected_year,
            quarter=selected_quarter,
            accession_number=normalized_accession,
        )
        selected = selection.filing
        if selected is None:
            return fd_error(
                "not_found",
                f"No {normalized_type} filing matches ticker {symbol}, year {selected_year}"
                + (f", quarter {selected_quarter}." if selected_quarter is not None else "."),
            )
        try:
            source_url = validate_sec_url(selected.source_url)
        except ValueError as error:
            raise UpstreamError("upstream_error", str(error)) from error
        scrape_run = await self._call(
            "get_filing_items", "context.dev", "/web/scrape/markdown",
            query_params={
                "url": source_url,
                "includeLinks": False,
                "includeImages": False,
                "useMainContentOnly": True,
                "timeoutMS": 30_000,
            },
        )
        markdown, _meta = parse_scrape_payload(scrape_run.output, source_url)
        sections = parse_filing_sections(markdown, normalized_type, selected_item)
        items = [
            filing_item_record(
                number=str(section["item"]),
                name=str(section["title"]),
                text=str(section["content"]),
            )
            for section in sections
        ]
        if selected_item is not None and not items:
            return fd_error(
                "not_found",
                f"Filing {selected.accession_number} has no item {selected_item.name}.",
            )
        report_day = date.fromisoformat(selected.report_date)
        return filing_items_response(
            resource=source_url,
            ticker=symbol,
            filing_type=selected.form,
            accession_number=selected.accession_number or None,
            year=report_day.year,
            quarter=((report_day.month - 1) // 3) + 1 if normalized_type != "10-K" else None,
            items=items,
        )

    @_fd_errors
    async def list_filing_item_types(self, filing_type: str | None = None) -> JsonObject:
        from monid_finance_mcp.providers.us.filing_items import (
            CATALOGS,
            FilingType,
            validate_catalog_filing_type,
        )

        normalized: FilingType | None = None
        if filing_type is not None:
            normalized = validate_catalog_filing_type(filing_type)
        types: tuple[FilingType, ...] = (
            (normalized,) if normalized is not None else ("10-K", "10-Q", "8-K")
        )
        response: JsonObject = {}
        for entry_type in types:
            catalog = CATALOGS[entry_type]
            response[catalog.filing_type] = [
                {"name": item.name, "title": item.title, "description": item.title}
                for item in catalog.items
            ]
        return response




    async def _earnings_for_ticker(
        self, tool: str, ticker: str, *, limit: int
    ) -> list[JsonObject]:
        """Compose earnings records for one ticker from statements + filings."""
        statements_run = await self._call(
            tool,
            DEFILLAMA,
            STATEMENTS_ENDPOINT,
            query_params={"ticker": ticker, "country": "US"},
        )
        filings_run = await self._call(
            tool,
            DEFILLAMA,
            FILINGS_ENDPOINT,
            query_params={"ticker": ticker, "country": "US"},
        )
        filings_rows = normalize_filings(
            filings_run.output,
            filing_types=None,
            limit=10_000,
            filing_date_gte=None,
            filing_date_lte=None,
        )
        return normalize_earnings(
            statements_run.output, filings_rows, ticker=ticker, limit=limit
        ).records

    @_fd_errors
    async def get_segmented_financials(
        self,
        ticker: str | None = None,
        period: str = "annual",
        limit: int = 10,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        cursor: str | None = None,
    ) -> JsonObject:
        if ticker is None:
            raise InputError("ticker is required")
        symbol = validate_ticker(ticker)
        normalized_period = validate_period(period)
        if normalized_period != "annual":
            raise InputError(
                "period must be annual: the validated route extracts the annual 10-K"
            )
        bounded_limit = validate_limit(limit, maximum=100)
        report = _date_filters(
            report_period,
            report_period_gte,
            report_period_lte,
            report_period_gt,
            report_period_lt,
            prefix="report_period",
        )
        offset = decode_cursor(cursor)[0] if cursor is not None else 0

        filings_run = await self._call(
            "get_segmented_financials",
            DEFILLAMA,
            FILINGS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        filings_rows = normalize_filings(
            filings_run.output,
            filing_types=None,
            limit=10_000,
            filing_date_gte=None,
            filing_date_lte=None,
        )
        filing = _latest_ten_k(filings_rows)
        if filing is None:
            return fd_error(
                "not_found",
                f"No 10-K filing exists for ticker {symbol}.",
            )
        filing_url = _opt_str(filing.get("primary_document_url")) or ""
        accession = derive_accession(filing_url)
        extract_run = await self._call(
            "get_segmented_financials",
            "context.dev",
            EXTRACT_ENDPOINT,
            body=extract_request(
                url=filing_url,
                schema=segment_extract_schema(),
                instructions=SEGMENT_INSTRUCTIONS,
            ),
        )
        data = parse_extract_output(extract_run.output, expected_url=filing_url)
        records = normalize_segmented_financials(
            data,
            ticker=symbol,
            filing_url=filing_url,
            accession_number=accession,
        )
        records = [
            record
            for record in records
            if _segment_matches(record, report)
        ][:bounded_limit]
        page = paginate(
            records, offset=offset, path="/financials/income-statements/segments"
        )
        return list_response("segmented_financials", page.records, page.next_url)

    @_fd_errors
    async def get_kpi_metrics(
        self,
        ticker: str | None = None,
        period: str = "quarterly",
        metric_name: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        limit: int = 4,
        cursor: str | None = None,
    ) -> JsonObject:
        return await self._kpi_extract_response(
            tool="get_kpi_metrics",
            response_key="kpi_metrics",
            path="/kpi/metrics",
            schema=kpi_metrics_extract_schema(),
            instructions=KPI_METRICS_INSTRUCTIONS,
            normalize=_normalize_kpi_metrics,
            ticker=ticker,
            period=period,
            metric_name=metric_name,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
            limit=limit,
            cursor=cursor,
        )

    @_fd_errors
    async def get_kpi_guidance(
        self,
        ticker: str | None = None,
        period: str = "quarterly",
        metric_name: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        limit: int = 4,
        cursor: str | None = None,
    ) -> JsonObject:
        return await self._kpi_extract_response(
            tool="get_kpi_guidance",
            response_key="kpi_guidance",
            path="/kpi/guidance",
            schema=kpi_guidance_extract_schema(),
            instructions=KPI_GUIDANCE_INSTRUCTIONS,
            normalize=_normalize_kpi_guidance,
            ticker=ticker,
            period=period,
            metric_name=metric_name,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
            limit=limit,
            cursor=cursor,
        )

    @_fd_errors
    async def get_kpi_non_gaap(
        self,
        ticker: str | None = None,
        period: str = "quarterly",
        metric_name: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        limit: int = 4,
        cursor: str | None = None,
    ) -> JsonObject:
        return await self._kpi_extract_response(
            tool="get_kpi_non_gaap",
            response_key="kpi_non_gaap",
            path="/kpi/non-gaap",
            schema=kpi_nongaap_extract_schema(),
            instructions=KPI_NONGAAP_INSTRUCTIONS,
            normalize=_normalize_kpi_nongaap,
            ticker=ticker,
            period=period,
            metric_name=metric_name,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
            limit=limit,
            cursor=cursor,
        )

    @_fd_errors
    async def _kpi_extract_response(
        self,
        *,
        tool: str,
        response_key: str,
        path: str,
        schema: JsonObject,
        instructions: str,
        normalize: Callable[..., list[JsonObject]],
        ticker: str | None,
        period: str,
        metric_name: str | None,
        report_period_gte: str | None,
        report_period_lte: str | None,
        limit: int,
        cursor: str | None,
    ) -> JsonObject:
        if ticker is None:
            raise InputError("ticker is required")
        symbol = validate_ticker(ticker)
        normalized_period = validate_kpi_period(period)
        bounded_limit = validate_limit(limit, maximum=50)
        gte = validate_date(report_period_gte, "report_period_gte")
        lte = validate_date(report_period_lte, "report_period_lte")
        offset = decode_cursor(cursor)[0] if cursor is not None else 0

        filings_run = await self._call(
            tool,
            DEFILLAMA,
            FILINGS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        filings_rows = normalize_filings(
            filings_run.output,
            filing_types=None,
            limit=10_000,
            filing_date_gte=None,
            filing_date_lte=None,
        )
        filing = _latest_kpi_filing(filings_rows, annual=normalized_period == "annual")
        if filing is None:
            return fd_error(
                "not_found",
                f"No {'10-K' if normalized_period == 'annual' else '10-Q'} "
                f"filing exists for ticker {symbol}.",
            )
        report_day = _opt_date(filing.get("report_date"))
        if report_day is not None and (
            (gte is not None and report_day < gte)
            or (lte is not None and report_day > lte)
        ):
            return list_response(response_key, [], None)
        filing_url = _opt_str(filing.get("primary_document_url")) or ""
        extract_run = await self._call(
            tool,
            "context.dev",
            EXTRACT_ENDPOINT,
            body=extract_request(url=filing_url, schema=schema, instructions=instructions),
        )
        data = parse_extract_output(extract_run.output, expected_url=filing_url)
        records = normalize(
            data,
            ticker=symbol,
            filing_url=filing_url,
            period=normalized_period,
            metric_name=metric_name,
        )[:bounded_limit]
        page = paginate(records, offset=offset, path=path)
        return list_response(response_key, page.records, page.next_url)

    @_fd_errors
    async def get_interest_rates(self) -> JsonObject:
        records: list[JsonObject] = []
        for spec in BANK_SPECS:
            try:
                run = await self._call(
                    "get_interest_rates", "context.dev", SCRAPE_ENDPOINT,
                    query_params=bank_scrape_query(spec.url),
                )
                markdown = parse_bank_scrape(run.output, expected_url=spec.url)
                rate = parse_policy_rate(markdown, bank=spec.bank)
            except (UpstreamError, SchemaDriftError):
                continue
            if rate is None:
                continue
            records.append(
                interest_rate_record(
                    bank=rate.bank, name=rate.name, rate=rate.rate, date=rate.date
                )
            )
        return interest_rates_response(records)

    @_fd_errors
    async def get_index_fund(
        self,
        ticker: str | None = None,
        as_of: str | None = None,
        asset_class: str | None = None,
        limit: int = 50,
        offset: int = 0,
    ) -> JsonObject:
        if ticker is None:
            raise InputError("ticker is required")
        symbol = validate_ticker(ticker)
        as_of_date = validate_date(as_of, "as_of")
        normalized_class = validate_asset_class(asset_class)
        bounded_limit = validate_limit(limit, maximum=1000)
        if offset < 0:
            raise InputError("offset must be a non-negative integer")

        search_run = await self._call(
            "get_index_fund", "context.dev", INDEX_FUND_SEARCH_ENDPOINT,
            body=index_fund_search_request(symbol),
        )
        candidates = pick_holdings_candidates(search_run.output, ticker=symbol)
        markdown: str | None = None
        title: str | None = None
        for candidate in candidates[:3]:
            url = _opt_str(candidate.get("url"))
            if url is None:
                continue
            try:
                scrape_run = await self._call(
                    "get_index_fund", "context.dev", SCRAPE_ENDPOINT,
                    query_params=fund_scrape_query(url),
                )
                page_markdown = parse_fund_scrape(scrape_run.output, expected_url=url)
            except (UpstreamError, SchemaDriftError):
                continue
            if parse_holdings(page_markdown):
                markdown = page_markdown
                title = _opt_str(candidate.get("title"))
                break
        if markdown is None:
            return fd_error(
                "bad_request",
                f"holdings document not routable for {symbol}",
            )
        holdings = parse_holdings(markdown)
        if normalized_class is not None:
            holdings = [
                holding
                for holding in holdings
                if holding.get("asset_class") == normalized_class
            ]
        document_as_of = parse_as_of(markdown)
        if as_of_date is not None and document_as_of is not None:
            document_day = _opt_date(document_as_of)
            if document_day is not None and document_day > as_of_date:
                return fd_error(
                    "not_found",
                    f"No holdings composition in effect on or before {as_of} "
                    f"is routable for {symbol}.",
                )
        page_holdings = holdings[offset : offset + bounded_limit]
        fund: JsonObject = {}
        _set_opt(fund, "name", title)
        _set_opt(fund, "as_of", document_as_of)
        fund["source"] = "public fund holdings fact sheet (markdown)"
        fund["total_holdings"] = len(holdings)
        fund["returned"] = len(page_holdings)
        fund["offset"] = offset
        return index_fund_response(symbol, fund, page_holdings)

    @_fd_errors
    async def get_institutional_holdings(
        self,
        ticker: str | None = None,
        filer_cik: str | None = None,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        limit: int = 10,
        cursor: str | None = None,
    ) -> JsonObject:
        if filer_cik is not None:
            return fd_error(
                "bad_request", "filer_cik lookup is not routed; pass ticker instead"
            )
        if ticker is None:
            raise InputError("ticker is required")
        symbol = validate_ticker(ticker)
        bounded_limit = validate_limit(limit, maximum=200)
        report = _date_filters(
            report_period,
            report_period_gte,
            report_period_lte,
            report_period_gt,
            report_period_lt,
            prefix="report_period",
        )
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        run = await self._call(
            "get_institutional_holdings",
            SECFORM4,
            INSTITUTIONAL_ENDPOINT,
            query_params={"ticker": symbol},
        )
        records = normalize_institutional_holdings(
            run.output,
            ticker=symbol,
            limit=bounded_limit,
            report_period=report,
        )
        page = paginate(records, offset=offset, path="/institutional-holdings")
        return institutional_holdings_response(symbol, page.records, page.next_url)





def _set_opt(record: JsonObject, key: str, value: JsonValue) -> None:
    """Set an optional record field only when a sourced value exists."""
    if value is None:
        return
    record[key] = value


def _earnings_filing_sort_key(record: JsonObject) -> tuple[bool, str]:
    filing_day = _opt_str(record.get("filing_date"))
    return filing_day is not None, filing_day or ""


def _latest_ten_k(filings_rows: list[JsonObject]) -> JsonObject | None:
    """The most recent SEC-valid 10-K filing row, by report then filing date."""
    candidates: list[tuple[date, date, JsonObject]] = []
    for record in filings_rows:
        form = _opt_str(record.get("form"))
        if form is None or form.upper() != "10-K":
            continue
        url = _opt_str(record.get("primary_document_url"))
        if url is None:
            continue
        try:
            validate_sec_url(url)
        except ValueError:
            continue
        report_day = _opt_date(record.get("report_date"))
        filing_day = _opt_date(record.get("filing_date"))
        if report_day is None or filing_day is None:
            continue
        candidates.append((report_day, filing_day, record))
    if not candidates:
        return None
    candidates.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return candidates[0][2]


def _latest_kpi_filing(
    filings_rows: list[JsonObject], *, annual: bool
) -> JsonObject | None:
    """The most recent SEC-valid 10-K (annual) or 10-Q (quarterly) filing."""
    wanted = "10-K" if annual else "10-Q"
    candidates: list[tuple[date, date, JsonObject]] = []
    for record in filings_rows:
        form = _opt_str(record.get("form"))
        if form is None or form.upper() != wanted:
            continue
        url = _opt_str(record.get("primary_document_url"))
        if url is None:
            continue
        try:
            validate_sec_url(url)
        except ValueError:
            continue
        report_day = _opt_date(record.get("report_date"))
        filing_day = _opt_date(record.get("filing_date"))
        if report_day is None or filing_day is None:
            continue
        candidates.append((report_day, filing_day, record))
    if not candidates:
        return None
    candidates.sort(key=lambda item: (item[0], item[1]), reverse=True)
    return candidates[0][2]


def _segment_matches(
    record: JsonObject, report: dict[str, date | None]
) -> bool:
    day = _opt_date(record.get("report_period"))
    if day is None:
        return False
    return _date_matches(
        day,
        exact=report["exact"],
        gte=report["gte"],
        lte=report["lte"],
        gt=report["gt"],
        lt=report["lt"],
    )


def _normalize_kpi_metrics(
    data: JsonObject,
    *,
    ticker: str,
    filing_url: str,
    period: str,
    metric_name: str | None,
) -> list[JsonObject]:
    return normalize_kpi_metrics(
        data,
        ticker=ticker,
        filing_url=filing_url,
        period=period,
        metric_name=metric_name,
    )


def _normalize_kpi_guidance(
    data: JsonObject,
    *,
    ticker: str,
    filing_url: str,
    period: str,
    metric_name: str | None,
) -> list[JsonObject]:
    return normalize_kpi_guidance(
        data,
        ticker=ticker,
        filing_url=filing_url,
        period=period,
        metric_name=metric_name,
    )


def _normalize_kpi_nongaap(
    data: JsonObject,
    *,
    ticker: str,
    filing_url: str,
    period: str,
    metric_name: str | None,
) -> list[JsonObject]:
    return normalize_kpi_nongaap(
        data,
        ticker=ticker,
        filing_url=filing_url,
        period=period,
        metric_name=metric_name,
    )



def _metric_string(value: object) -> str | None:
    """Render a screener metric value as a clean string."""
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    if isinstance(value, float):
        if value.is_integer():
            return str(int(value))
        return str(round(value, 6))
    return str(value)


def _cache_key(
    provider: str,
    endpoint: str,
    body: JsonObject | None,
    query_params: JsonObject | None,
) -> tuple[str, str, str, str]:
    """A hashable cache key for one Monid call."""
    return (
        provider,
        endpoint,
        json.dumps(body, sort_keys=True, separators=(",", ":")) if body else "",
        json.dumps(query_params, sort_keys=True, separators=(",", ":"))
        if query_params
        else "",
    )


def _opt_num(value: JsonValue) -> float | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    return float(value)


def _date_matches(
    value: date,
    *,
    exact: date | None,
    gte: date | None,
    lte: date | None,
    gt: date | None,
    lt: date | None,
) -> bool:
    if exact is not None and value != exact:
        return False
    if gte is not None and value < gte:
        return False
    if lte is not None and value > lte:
        return False
    if gt is not None and value <= gt:
        return False
    return lt is None or value < lt


_METRICS_KEY_ORDER: tuple[str, ...] = (
    "ticker",
    "report_period",
    "fiscal_period",
    "period",
    "currency",
    "accession_number",
    "form_type",
    "filing_url",
    "filing_date",
    "filing_datetime",
    "enterprise_value",
    "price_to_earnings_ratio",
    "price_to_book_ratio",
    "price_to_sales_ratio",
    "enterprise_value_to_ebitda_ratio",
    "enterprise_value_to_revenue_ratio",
    "free_cash_flow_yield",
    "peg_ratio",
    "gross_margin",
    "operating_margin",
    "net_margin",
    "return_on_equity",
    "return_on_assets",
    "return_on_invested_capital",
    "asset_turnover",
    "inventory_turnover",
    "receivables_turnover",
    "days_sales_outstanding",
    "operating_cycle",
    "working_capital_turnover",
    "current_ratio",
    "quick_ratio",
    "cash_ratio",
    "operating_cash_flow_ratio",
    "debt_to_equity",
    "debt_to_assets",
    "interest_coverage",
    "revenue_growth",
    "earnings_growth",
    "book_value_growth",
    "earnings_per_share_growth",
    "free_cash_flow_growth",
    "operating_income_growth",
    "ebitda_growth",
    "payout_ratio",
    "earnings_per_share",
    "book_value_per_share",
    "free_cash_flow_per_share",
)


def _ordered_metrics_record(record: JsonObject) -> JsonObject:
    """Re-key a metrics record into Financial Datasets property order."""
    ordered: JsonObject = {}
    for key in _METRICS_KEY_ORDER:
        if key in record:
            ordered[key] = record[key]
    for key, value in record.items():
        if key not in ordered:
            ordered[key] = value
    return ordered


def _statement_record(
    *,
    statement: str,
    ticker: str,
    period: str,
    row: PeriodRow,
    identity: FilingIdentity | None,
    fiscal_period: str | None,
) -> JsonObject:
    builder = {
        "income": income_statement_record,
        "balance": balance_sheet_record,
        "cash": cash_flow_record,
    }[statement]
    return builder(
        ticker=ticker,
        period=period,
        report_period=row.report_period.isoformat(),
        fiscal_period=fiscal_period,
        values=row.values,
        identity=identity,
    )


def _apply_filing_filters(
    rows: list[PeriodRow],
    identity_map: dict[date, FilingIdentity] | None,
    filing: dict[str, date | None],
) -> list[PeriodRow]:
    """Filter rows on joined filing dates; identity-less rows drop when filtering."""
    if not any(value is not None for value in filing.values()):
        return rows
    if identity_map is None:
        return []

    def keep(row: PeriodRow) -> bool:
        identity = identity_map.get(row.report_period)
        if identity is None or identity.filing_date is None:
            return False
        day = identity.filing_date
        exact = filing.get("exact")
        minimum = filing.get("gte")
        maximum = filing.get("lte")
        greater = filing.get("gt")
        less = filing.get("lt")
        if exact is not None and day != exact:
            return False
        if minimum is not None and day < minimum:
            return False
        if maximum is not None and day > maximum:
            return False
        if greater is not None and day <= greater:
            return False
        return less is None or day < less

    return [row for row in rows if keep(row)]


def _date_filters(
    exact: str | None,
    gte: str | None,
    lte: str | None,
    gt: str | None,
    lt: str | None,
    *,
    prefix: str,
) -> dict[str, date | None]:
    return {
        "exact": validate_date(exact, prefix),
        "gte": validate_date(gte, f"{prefix}_gte"),
        "lte": validate_date(lte, f"{prefix}_lte"),
        "gt": validate_date(gt, f"{prefix}_gt"),
        "lt": validate_date(lt, f"{prefix}_lt"),
    }


def _bar_time(bar: JsonObject, interval: str) -> str:
    """The human-readable UTC time label of one price bar."""
    if interval == "day":
        return str(bar["date"])
    return str(bar["end_date"])


def _as_ratio(value: float | None) -> float | None:
    """DefiLlama reports some margins as percentages; FD expects ratios."""
    if value is None:
        return None
    return value / 100 if abs(value) > 1.5 else value


def _ratio(numerator: JsonValue, denominator: JsonValue) -> float | None:
    top, bottom = _num(numerator), _num(denominator)
    if top is None or bottom is None or bottom == 0:
        return None
    return top / bottom


def _num(value: JsonValue) -> float | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    return float(value)


def _opt_str(value: JsonValue) -> str | None:
    return value if isinstance(value, str) and value else None


def _opt_date(value: JsonValue) -> date | None:
    text = _opt_str(value)
    if text is None:
        return None
    try:
        return date.fromisoformat(text[:10])
    except ValueError:
        return None




def _latest_filing_year(
    value: JsonValue,
    *,
    filing_type: str,
    quarter: int | None,
    accession_number: str | None,
) -> int | None:
    """Return the newest year with a filing matching the year-optional request.

    Used only when get_filing_items receives year=None; select_filing then
    performs the authoritative selection within the resolved year.
    """
    best: int | None = None
    for record in _filing_records(value):
        form = record.get("form")
        if not isinstance(form, str) or form.strip().upper() != filing_type:
            continue
        report_date = record.get("reportDate")
        if not isinstance(report_date, str):
            continue
        try:
            day = date.fromisoformat(report_date[:10])
        except ValueError:
            continue
        if quarter is not None and ((day.month - 1) // 3) + 1 != quarter:
            continue
        if accession_number is not None:
            source_url = record.get("primaryDocumentUrl")
            if not isinstance(source_url, str) or derive_accession(source_url) != accession_number:
                continue
        if best is None or day.year > best:
            best = day.year
    return best


def _filing_records(value: JsonValue) -> list[JsonObject]:
    """Unwrap a DefiLlama filings payload into a list of filing rows."""
    current = value
    for _ in range(4):
        if isinstance(current, list):
            rows: list[JsonObject] = []
            for record in current:
                if isinstance(record, dict):
                    rows.append(record)
            return rows
        if not isinstance(current, dict):
            break
        for key in ("data", "filings"):
            child = current.get(key)
            if isinstance(child, list | dict):
                current = child
                break
        else:
            break
    return []
