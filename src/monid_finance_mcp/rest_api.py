"""Financial Datasets-compatible REST API surface backed by FinanceService.

Exposes the exact Financial Datasets path and query-parameter contract over
FastAPI. Every route is protected by the ``X-API-KEY`` header and returns the
Financial Datasets ErrorResponse shape (``{"error", "message"}``) on failure
with the matching HTTP status. Pagination links returned by the service are
rewritten to this deployment's own base URL so clients can walk pages against
us instead of the placeholder upstream host.
"""
from __future__ import annotations

import os
from collections.abc import AsyncGenerator
from contextlib import asynccontextmanager
from pathlib import Path
from urllib.parse import parse_qs, urlencode, urlsplit

from fastapi import Depends, FastAPI, Header, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, Field
from starlette.applications import Starlette
from starlette.routing import Route

from monid_finance_mcp.client import MonidClient
from monid_finance_mcp.models import JsonObject
from monid_finance_mcp.server import mcp
from monid_finance_mcp.service import FinanceService

DEFAULT_BASE_URL = "http://localhost:8000"
UNAUTHORIZED_MESSAGE = "Missing or invalid API key."
NOT_IMPLEMENTED_MESSAGE = (
    "This Financial Datasets route is not implemented by the Monid-backed "
    "server yet; the call was free and no data was fabricated."
)

# The FastMCP server exposes its Streamable HTTP handler as a standalone
# Starlette app whose lifespan owns a session-manager task group. Mounting that
# app inside FastAPI would skip the nested lifespan, so we serve its MCP server
# through our own session manager and run that manager from the FastAPI
# lifespan instead.
def _make_mcp_session_manager():
    from mcp.server.streamable_http_manager import StreamableHTTPSessionManager

    return StreamableHTTPSessionManager(
        app=mcp._mcp_server,  # type: ignore[attr-defined]
        event_store=mcp._event_store,  # type: ignore[attr-defined]
        json_response=mcp.settings.json_response,
        stateless=mcp.settings.stateless_http,
    )

# Financial Datasets error codes emitted by the service and their HTTP status.
_ERROR_STATUS = {
    "bad_request": 400,
    "invalid_cursor": 400,
    "unauthorized": 401,
    "not_found": 404,
    "not_implemented": 200,
}
_DEFAULT_ERROR_STATUS = 500


class _APIError(Exception):
    """A controlled API error carrying the FD ErrorResponse payload."""

    def __init__(self, status: int, code: str, message: str) -> None:
        super().__init__(message)
        self.status = status
        self.payload: JsonObject = {"error": code, "message": message}


class _SearchRequest(BaseModel):
    """The POST body for ``/financials/search/screener``."""

    filters: list[JsonObject]
    limit: int = Field(default=10, ge=1)


def _fd_error(status: int, code: str, message: str) -> JSONResponse:
    return JSONResponse({"error": code, "message": message}, status_code=status)


def _rebuild_next_page_url(service_url: str, base: str) -> str:
    """Point a service pagination link at this deployment's base URL."""
    parts = urlsplit(service_url)
    token = parse_qs(parts.query).get("cursor")
    cursor = token[0] if token else None
    path = parts.path
    root = base.rstrip("/")
    if cursor:
        return f"{root}{path}?{urlencode({'cursor': cursor})}"
    return f"{root}{path}"


def _rewrite_next_page_url(payload: JsonObject, base: str) -> JsonObject:
    """Return a copy of the payload with ``next_page_url`` rebound to base."""
    current = payload.get("next_page_url")
    if not isinstance(current, str) or not current:
        return payload
    rewritten = dict(payload)
    rewritten["next_page_url"] = _rebuild_next_page_url(current, base)
    return rewritten


def _map_error_status(code: str) -> int:
    return _ERROR_STATUS.get(code, _DEFAULT_ERROR_STATUS)


def _finish(payload: JsonObject, base: str) -> JSONResponse:
    """Turn a service response into an HTTP response with FD error mapping."""
    code = payload.get("error")
    if isinstance(code, str):
        return _fd_error(_map_error_status(code), code, str(payload.get("message", "")))
    if isinstance(payload.get("next_page_url"), str):
        return JSONResponse(_rewrite_next_page_url(payload, base), status_code=200)
    return JSONResponse(payload, status_code=200)


def _api_keys_from_env() -> tuple[str, ...] | None:
    """The allowed API keys, or None when any non-empty key is accepted."""
    raw = os.getenv("API_KEYS")
    if raw is None:
        return None
    return tuple(entry for entry in (part.strip() for part in raw.split(",")) if entry)


def create_app(
    service: FinanceService | None = None,
    *,
    api_keys: tuple[str, ...] | None = None,
    base_url: str | None = None,
) -> FastAPI:
    """Build the FastAPI application, optionally with an injected service."""
    finance = service or FinanceService(
        MonidClient(
            cli=os.getenv("MONID_CLI", "monid"),
            run_timeout_seconds=_run_timeout(),
            allowlist_path=_allowlist_path(),
        )
    )
    allowed_keys = _api_keys_from_env() if api_keys is None else api_keys
    root = (base_url or os.getenv("MONID_API_BASE_URL") or DEFAULT_BASE_URL).rstrip("/")

    session_manager = _make_mcp_session_manager()

    @asynccontextmanager
    async def lifespan(_: FastAPI) -> AsyncGenerator[None, None]:
        async with session_manager.run():
            yield

    app = FastAPI(title="Monid Finance REST", version="0.1.0", lifespan=lifespan)

    @app.exception_handler(_APIError)
    async def _on_api_error(_: Request, exc: _APIError) -> JSONResponse:
        return JSONResponse(exc.payload, status_code=exc.status)

    def _authorize(x_api_key: str | None = Header(None, alias="X-API-KEY")) -> None:
        if not x_api_key or (allowed_keys is not None and x_api_key not in allowed_keys):
            raise _APIError(401, "unauthorized", UNAUTHORIZED_MESSAGE)

    auth = [Depends(_authorize)]

    # ---- Financial statements ----
    @app.get("/financials/income-statements", dependencies=auth)
    async def income_statements(
        ticker: str | None = None,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        as_reported: bool = False,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get income statements for a ticker."""
        if as_reported:
            return _fd_error(
                400,
                "bad_request",
                "as_reported is not supported; use the normalized income-statements endpoint",
            )
        return _finish(
            await finance.get_income_statement(
                ticker=ticker,
                period=period,
                limit=limit,
                report_period=report_period,
                report_period_gte=report_period_gte,
                report_period_lte=report_period_lte,
                report_period_gt=report_period_gt,
                report_period_lt=report_period_lt,
                filing_date=None,
                cursor=cursor,
            ),
            root,
        )

    @app.get("/financials/balance-sheets", dependencies=auth)
    async def balance_sheets(
        ticker: str | None = None,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        as_reported: bool = False,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get balance sheets for a ticker."""
        if as_reported:
            return _fd_error(
                400,
                "bad_request",
                "as_reported is not supported; use the normalized balance-sheets endpoint",
            )
        return _finish(
            await finance.get_balance_sheet(
                ticker=ticker,
                period=period,
                limit=limit,
                report_period=report_period,
                report_period_gte=report_period_gte,
                report_period_lte=report_period_lte,
                report_period_gt=report_period_gt,
                report_period_lt=report_period_lt,
                filing_date=None,
                cursor=cursor,
            ),
            root,
        )

    @app.get("/financials/cash-flow-statements", dependencies=auth)
    async def cash_flow_statements(
        ticker: str | None = None,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        as_reported: bool = False,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get cash flow statements for a ticker."""
        if as_reported:
            return _fd_error(
                400,
                "bad_request",
                "as_reported is not supported; use the normalized cash-flow-statements endpoint",
            )
        return _finish(
            await finance.get_cash_flow_statement(
                ticker=ticker,
                period=period,
                limit=limit,
                report_period=report_period,
                report_period_gte=report_period_gte,
                report_period_lte=report_period_lte,
                report_period_gt=report_period_gt,
                report_period_lt=report_period_lt,
                filing_date=None,
                cursor=cursor,
            ),
            root,
        )

    # ---- Financial metrics ----
    @app.get("/financial-metrics", dependencies=auth)
    async def financial_metrics(
        ticker: str | None = None,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
        report_period_gt: str | None = None,
        report_period_lt: str | None = None,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get historical financial metrics and ratios for a ticker."""
        return _finish(
            await finance.get_financial_metrics(
                ticker=ticker,
                period=period,
                limit=limit,
                report_period=report_period,
                report_period_gte=report_period_gte,
                report_period_lte=report_period_lte,
                report_period_gt=report_period_gt,
                report_period_lt=report_period_lt,
                filing_date=None,
                cursor=cursor,
            ),
            root,
        )

    @app.get("/financial-metrics/snapshot", dependencies=auth)
    async def financial_metrics_snapshot(ticker: str | None = None) -> JSONResponse:
        """Get a snapshot of the latest financial metrics for a ticker."""
        return _finish(await finance.get_financial_metrics_snapshot(ticker=ticker), root)

    # ---- Earnings ----
    @app.get("/earnings", dependencies=auth)
    async def earnings(
        ticker: str | None = None,
        limit: int = 1,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get earnings data for a ticker."""
        return _finish(await finance.get_earnings(ticker=ticker, limit=limit, cursor=cursor), root)

    # ---- Filings ----
    @app.get("/filings", dependencies=auth)
    async def filings(
        ticker: str | None = None,
        cik: str | None = None,
        filing_type: list[str] | None = None,
        limit: int = 10,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get the most recent SEC filings for a company."""
        return _finish(
            await finance.get_filings(
                ticker=ticker, cik=cik, filing_type=filing_type, limit=limit, cursor=cursor
            ),
            root,
        )

    # ---- Prices ----
    @app.get("/prices", dependencies=auth)
    async def prices(
        ticker: str | None = None,
        interval: str = "day",
        interval_multiplier: int = 1,
        start_date: str | None = None,
        end_date: str | None = None,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get stock price data for a company over a date range."""
        if interval_multiplier != 1:
            return _fd_error(
                400,
                "bad_request",
                "interval_multiplier must be 1 (multi-bar intervals are not implemented)",
            )
        if ticker is None:
            return _fd_error(400, "bad_request", "ticker is required")
        return _finish(
            await finance.get_stock_prices(
                ticker=ticker,
                interval=interval,
                start_date=start_date,
                end_date=end_date,
                cursor=cursor,
            ),
            root,
        )

    @app.get("/prices/snapshot", dependencies=auth)
    async def price_snapshot(ticker: str | None = None) -> JSONResponse:
        """Get the latest stock price for a company."""
        if ticker is None:
            return _fd_error(400, "bad_request", "ticker is required")
        return _finish(await finance.get_stock_price(ticker), root)

    # ---- News ----
    @app.get("/news", dependencies=auth)
    async def news(
        ticker: str | None = None,
        limit: int = 5,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get the latest news for a company."""
        return _finish(await finance.get_news(ticker=ticker, limit=limit, cursor=cursor), root)

    # ---- Insider trades ----
    @app.get("/insider-trades", dependencies=auth)
    async def insider_trades(
        ticker: str | None = None,
        limit: int = 10,
        name: str | None = None,
        filing_date: str | None = None,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
        cursor: str | None = None,
    ) -> JSONResponse:
        """Get insider trading transactions for a company."""
        return _finish(
            await finance.get_insider_trades(
                ticker=ticker,
                limit=limit,
                name=name,
                filing_date=filing_date,
                filing_date_gte=filing_date_gte,
                filing_date_lte=filing_date_lte,
                cursor=cursor,
            ),
            root,
        )

    # ---- Screener ----
    @app.post("/financials/search/screener", dependencies=auth)
    async def screener(request: _SearchRequest) -> JSONResponse:
        """Screen stocks by company attributes."""
        return _finish(
            await finance.screen_stocks(filters=request.filters, limit=request.limit),
            root,
        )

    @app.get("/financials/search/screener/filters", dependencies=auth)
    async def screener_filters() -> JSONResponse:
        """List available stock screener filters and operators."""
        return _finish(await finance.list_stock_screener_filters(), root)

    # ---- Company facts ----
    @app.get("/company/facts", dependencies=auth)
    async def company_facts(
        ticker: str | None = None, cik: str | None = None
    ) -> JSONResponse:
        """Get company details such as name, sector, industry, and exchange."""
        return _finish(await finance.get_company_facts(ticker=ticker, cik=cik), root)

    # ---- Filing items ----
    @app.get("/filings/items", dependencies=auth)
    async def filing_items(
        ticker: str,
        filing_type: str,
        year: int,
        quarter: int | None = None,
        item: str | None = None,
        accession_number: str | None = None,
        include_exhibits: bool = False,
    ) -> JSONResponse:
        """Get individual items from a filing, such as Item 1 from a 10-K."""
        return _finish(
            await finance.get_filing_items(
                ticker=ticker,
                filing_type=filing_type,
                year=year,
                quarter=quarter,
                item=item,
                accession_number=accession_number,
                include_exhibits=include_exhibits,
            ),
            root,
        )

    # ---- Not-yet-implemented Financial Datasets routes ----
    def not_implemented() -> JSONResponse:
        return _fd_error(200, "not_implemented", NOT_IMPLEMENTED_MESSAGE)

    for path in (
        "/kpi/metrics",
        "/kpi/metrics/tickers",
        "/kpi/metrics/sectors",
        "/kpi/guidance",
        "/kpi/non-gaap",
        "/macro/interest-rates",
        "/macro/interest-rates/snapshot",
        "/macro/interest-rates/banks",
        "/financials/segments",
        "/financials/income-statements/segments",
        "/financials/balance-sheets/segments",
        "/financials/cash-flow-statements/segments",
        "/index-funds",
        "/index-funds/tickers",
        "/institutional-holdings",
        "/institutional-holdings/investors",
        "/institutional-holdings/tickers",
        "/insider-ownership",
        "/beneficial-ownership",
        "/activist-ownership",
    ):
        app.add_api_route(path, not_implemented, methods=["GET"], dependencies=auth)

    _ = (
        _on_api_error,
        income_statements,
        balance_sheets,
        cash_flow_statements,
        financial_metrics,
        financial_metrics_snapshot,
        earnings,
        filings,
        prices,
        price_snapshot,
        news,
        insider_trades,
        screener,
        screener_filters,
        company_facts,
        filing_items,
    )
    from mcp.server.fastmcp.server import StreamableHTTPASGIApp

    # Serve the MCP endpoint at both /mcp and /api like Financial Datasets.
    mcp_handler = StreamableHTTPASGIApp(session_manager)
    mcp_root = Starlette(routes=[Route("/", endpoint=mcp_handler)])
    app.mount("/mcp", mcp_root)
    app.mount("/api", mcp_root)

    return app


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


app = create_app()


def main() -> None:
    import uvicorn

    port = int(os.getenv("PORT", "8000"))
    uvicorn.run("monid_finance_mcp.rest_api:app", host="0.0.0.0", port=port)
