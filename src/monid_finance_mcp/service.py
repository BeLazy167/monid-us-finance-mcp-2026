"""Financial Datasets API contract service layer.

Every tool returns either a Financial Datasets response object or the
Financial Datasets ErrorResponse shape ``{"error", "message"}``.
Provenance, cost, and warnings live in the committed receipts ledger,
never inside responses.
"""
from __future__ import annotations

from collections.abc import Awaitable, Callable
from datetime import date
from functools import wraps
from typing import TypeVar

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
    list_response,
    metric_snapshot_record,
    news_record,
    price_record,
    price_snapshot_response,
    prices_response,
)
from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.filing_items import (
    derive_accession,
    normalize_accession,
    validate_sec_url,
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
from monid_finance_mcp.providers.us.statements import (
    PeriodRow,
    StatementSeries,
    derive_ttm_rows,
    filter_rows,
    fiscal_period_label,
    fiscal_year_end_month,
    parse_statement_series,
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
        self, client: MonidClientProtocol, ledger: ReceiptsLedger | None = None
    ) -> None:
        self._client = client
        self._ledger = ledger if ledger is not None else ReceiptsLedger()

    async def _call(
        self,
        tool: str,
        provider: str,
        endpoint: str,
        *,
        body: JsonObject | None = None,
        query_params: JsonObject | None = None,
    ) -> MonidRun:
        """Run one Monid call, record a receipt, and map failures to FD errors."""
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
        return metric_snapshot_record(
            ticker=symbol,
            market_cap=_num(summary.get("market_cap")),
            enterprise_value=_num(summary.get("enterprise_value")),
            trailing_pe=_num(summary.get("price_to_earnings_ratio")),
            price_to_book=_num(summary.get("price_to_book")),
            price_to_revenue=_num(summary.get("price_to_revenue")),
            ev_to_ebitda=_num(summary.get("enterprise_value_to_ebitda")),
            gross_margin_ttm=gross_margin,
            operating_margin_ttm=_num(summary.get("operating_profit_margin_ttm")),
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
            raise InputError("ticker is required (market-wide news is not supported)")
        symbol = validate_ticker(ticker)
        bounded_limit = validate_limit(limit, maximum=10)
        offset = decode_cursor(cursor)[0] if cursor is not None else 0
        run = await self._call(
            "get_news", NEWS_PROVIDER, NEWS_ENDPOINT,
            query_params={"ticker": symbol},
        )
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
    async def get_filing_items(
        self,
        ticker: str,
        filing_type: str,
        year: int,
        quarter: int | None = None,
        item: str | None = None,
        accession_number: str | None = None,
        include_exhibits: bool = False,
    ) -> JsonObject:
        from monid_finance_mcp.providers.us.filing_items import (
            parse_filing_sections,
            parse_scrape_payload,
            select_filing,
            validate_filing_item_request,
        )

        if include_exhibits:
            raise InputError(
                "include_exhibits is not supported: the validated route cannot "
                "identify and fetch filing exhibits"
            )
        symbol, normalized_type, selected_year, selected_quarter, selected_item = (
            validate_filing_item_request(ticker, filing_type, year, quarter, item)
        )
        normalized_accession = normalize_accession(accession_number)
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


