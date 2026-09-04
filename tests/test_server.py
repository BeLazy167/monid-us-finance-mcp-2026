from __future__ import annotations

from typing import cast

import pytest

from monid_finance_mcp.models import JsonObject
from monid_finance_mcp.server import create_server
from monid_finance_mcp.service import FinanceService

from .test_service import full_client

FD_TOOL_NAMES = {
    "get_beneficial_owners",
    "get_beneficial_ownership",
    "get_company_facts",
    "get_earnings",
    "get_financial_metrics",
    "get_financial_metrics_snapshot",
    "get_income_statement",
    "get_balance_sheet",
    "get_cash_flow_statement",
    "get_index_fund",
    "get_insider_ownership",
    "get_insider_trades",
    "get_institutional_investors",
    "get_institutional_holdings",
    "get_interest_rates",
    "get_kpi_guidance",
    "get_kpi_metrics",
    "get_kpi_non_gaap",
    "get_news",
    "get_filings",
    "get_filing_items",
    "list_filing_item_types",
    "get_segmented_financials",
    "get_stock_prices",
    "get_stock_price",
    "screen_stocks",
    "list_stock_screener_filters",
}


@pytest.mark.asyncio
async def test_server_registers_all_financial_datasets_tool_names() -> None:
    server = create_server(FinanceService(full_client()))

    tools = await server.list_tools()

    assert {tool.name for tool in tools} == FD_TOOL_NAMES
    assert len(tools) == 27


@pytest.mark.asyncio
async def test_get_filing_items_has_exact_public_input_contract() -> None:
    server = create_server(FinanceService(full_client()))

    tools = await server.list_tools()
    tool = next(item for item in tools if item.name == "get_filing_items")
    properties = cast(JsonObject, tool.inputSchema["properties"])
    assert list(properties) == [
        "ticker",
        "filing_type",
        "year",
        "quarter",
        "item",
        "accession_number",
        "include_exhibits",
    ]
    filing_type = properties["filing_type"]
    assert isinstance(filing_type, dict)
    assert filing_type["enum"] == ["10-K", "10-Q", "8-K"]
    assert tool.inputSchema["required"] == ["ticker", "filing_type", "year"]


@pytest.mark.asyncio
async def test_statement_tools_match_financial_datasets_parameters() -> None:
    server = create_server(FinanceService(full_client()))

    tools = await server.list_tools()
    tool = next(item for item in tools if item.name == "get_income_statement")
    properties = cast(JsonObject, tool.inputSchema["properties"])
    assert list(properties) == [
        "ticker",
        "period",
        "limit",
        "cik",
        "report_period",
        "report_period_gte",
        "report_period_lte",
        "report_period_gt",
        "report_period_lt",
        "filing_date",
        "filing_date_gte",
        "filing_date_lte",
        "filing_date_gt",
        "filing_date_lt",
        "cursor",
    ]
    period = properties["period"]
    assert isinstance(period, dict)
    assert period["enum"] == ["annual", "quarterly", "ttm"]
    assert period["default"] == "annual"


@pytest.mark.asyncio
async def test_stock_prices_input_contract() -> None:
    server = create_server(FinanceService(full_client()))

    tools = await server.list_tools()
    tool = next(item for item in tools if item.name == "get_stock_prices")
    properties = cast(JsonObject, tool.inputSchema["properties"])
    assert list(properties) == ["ticker", "interval", "start_date", "end_date", "cursor"]
    interval = properties["interval"]
    assert isinstance(interval, dict)
    assert interval["enum"] == ["day", "week", "month", "year"]


@pytest.mark.asyncio
async def test_stub_tools_answer_not_implemented_at_zero_cost() -> None:
    server = create_server(FinanceService(full_client()))

    result = await server.call_tool("get_index_fund", {"ticker": "SPY"})
    payload = _payload(result)
    assert payload == {
        "error": "not_implemented",
        "message": (
            "This Financial Datasets tool is not implemented by the Monid-backed "
            "server yet; the call was free and no data was fabricated."
        ),
    }
    ownership = await server.call_tool("get_beneficial_ownership", {"ticker": "AAPL"})
    assert _payload(ownership)["error"] == "not_implemented"


@pytest.mark.asyncio
async def test_working_tool_delegates_to_service() -> None:
    server = create_server(FinanceService(full_client()))

    result = await server.call_tool("get_stock_price", {"ticker": "AAPL"})
    payload = _payload(result)
    snapshot = payload["snapshot"]
    assert isinstance(snapshot, dict)
    assert snapshot["ticker"] == "AAPL"
    assert snapshot["price"] == 230.1


def _payload(result: object) -> JsonObject:
    import json
    from typing import Any, cast

    pair = cast(tuple[list[Any], Any], result)
    content = pair[0]
    first = content[0]
    text = cast(str, first.text)
    payload = json.loads(text)
    assert isinstance(payload, dict)
    return cast(JsonObject, payload)
