from __future__ import annotations

import json
from pathlib import Path
from typing import Any, cast

import pytest

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.server import create_server
from monid_finance_mcp.service import FinanceService

from .test_service import FakeClient, completed, full_client

FD_TOOLS = json.loads(
    (Path(__file__).resolve().parents[1] / "docs" / "fd-mcp-tools.json").read_text()
)


def _result(out: object) -> JsonValue:
    """The actual value a tool returned, read from structured output."""
    structured = cast(dict[str, Any], cast(tuple[list[Any], Any], out)[1])
    return cast(JsonValue, structured["result"])


def _as_records(value: JsonValue) -> list[JsonObject]:
    assert isinstance(value, list)
    records: list[JsonObject] = []
    for item in cast(list[object], value):
        assert isinstance(item, dict)
        records.append(cast(JsonObject, item))
    return records


@pytest.mark.asyncio
async def test_server_registers_all_financial_datasets_tool_names() -> None:
    server = create_server(FinanceService(full_client()))

    tools = await server.list_tools()

    assert {tool.name for tool in tools} == set(FD_TOOLS)
    assert len(tools) == 27


@pytest.mark.asyncio
async def test_every_tool_schema_matches_live_fd_mcp_contract() -> None:
    server = create_server(FinanceService(full_client()))

    tools = await server.list_tools()

    assert len(tools) == len(FD_TOOLS) == 27
    for tool in tools:
        spec = FD_TOOLS[tool.name]
        properties = cast(JsonObject, tool.inputSchema["properties"])
        assert list(properties) == spec["params"], tool.name
        required_object: object = tool.inputSchema.get("required") or []
        required = cast(list[str], required_object)
        assert list(required) == spec["required"], tool.name
        assert tool.description == spec["description"], tool.name


@pytest.mark.asyncio
async def test_list_tool_returns_bare_array_without_wrapper() -> None:
    server = create_server(FinanceService(full_client()))

    result = await server.call_tool("get_income_statement", {"ticker": "AAPL"})
    records = _as_records(_result(result))

    assert records
    assert all("report_period" in record for record in records)


@pytest.mark.asyncio
async def test_object_tool_returns_bare_object() -> None:
    server = create_server(FinanceService(full_client()))

    result = await server.call_tool("get_stock_price", {"ticker": "AAPL"})
    payload = cast(JsonObject, _result(result))

    assert isinstance(payload, dict)
    assert payload["ticker"] == "AAPL"
    assert payload["price"] == 230.1


@pytest.mark.asyncio
async def test_aggregation_follows_cursor_during_call_tool() -> None:
    many: list[JsonObject] = []
    for index in range(14):
        many.append(
            {
                "filingDate": f"2026-01-{index + 1:02d}",
                "reportDate": "2025-12-31",
                "form": "8-K",
                "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/x.htm",
            }
        )
    client = FakeClient(
        {("defillama", "/equities/v1/filings"): completed("/equities/v1/filings", many)}
    )
    server = create_server(FinanceService(client))

    result = await server.call_tool("get_filings", {"ticker": "AAPL", "limit": 14})
    records = _as_records(_result(result))

    assert len(records) == 14
    # The TTL run cache serves page two from the first upstream payload, so the
    # aggregation costs a single Monid call.
    assert len(client.calls) == 1


@pytest.mark.asyncio
async def test_stub_tools_answer_not_implemented_at_zero_cost() -> None:
    server = create_server(FinanceService(full_client()))

    result = await server.call_tool("get_index_fund", {"ticker": "SPY"})
    payload = cast(JsonObject, _result(result))
    assert payload == {
        "error": "not_implemented",
        "message": (
            "This Financial Datasets tool is not implemented by the Monid-backed "
            "server yet; the call was free and no data was fabricated."
        ),
    }
    ownership = await server.call_tool("get_beneficial_ownership", {"ticker": "AAPL"})
    assert cast(JsonObject, _result(ownership))["error"] == "not_implemented"


@pytest.mark.asyncio
async def test_as_reported_is_rejected_honestly() -> None:
    server = create_server(FinanceService(full_client()))

    for tool in ("get_income_statement", "get_balance_sheet", "get_cash_flow_statement"):
        result = await server.call_tool(tool, {"ticker": "AAPL", "as_reported": True})
        payload = cast(JsonObject, _result(result))
        assert payload["error"] == "bad_request"
        assert "as_reported" in str(payload["message"])


@pytest.mark.asyncio
async def test_interval_multiplier_not_one_is_rejected_honestly() -> None:
    server = create_server(FinanceService(full_client()))

    result = await server.call_tool(
        "get_stock_prices", {"ticker": "AAPL", "interval_multiplier": 2}
    )
    payload = cast(JsonObject, _result(result))
    assert payload["error"] == "bad_request"
    assert "interval_multiplier" in str(payload["message"])


@pytest.mark.asyncio
async def test_non_usd_screen_currency_is_rejected_honestly() -> None:
    server = create_server(FinanceService(full_client()))

    result = await server.call_tool(
        "screen_stocks", {"filters": [{"field": "exchange", "value": "NASDAQ"}], "currency": "EUR"}
    )
    payload = cast(JsonObject, _result(result))
    assert payload["error"] == "bad_request"
    assert "USD" in str(payload["message"])


@pytest.mark.asyncio
async def test_filing_items_year_optional_via_call_tool() -> None:

    from .test_filing_items import (
        SCRAPE_ENDPOINT,
        client_with,
        completed,
        filing_row,
        markdown_payload,
    )

    SEC_URL_2 = "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250329.htm"
    filings = [
        filing_row(report_date="2024-06-29", filing_date="2024-08-01", form="10-Q", url=SEC_URL_2),
        filing_row(report_date="2025-06-28", filing_date="2025-08-01", form="10-Q"),
    ]
    markdown = "# PART I\n# ITEM 1. FINANCIAL STATEMENTS\nQuarterly statements.\n"
    client = client_with(markdown, filings=filings)
    client.outcomes[("context.dev", SCRAPE_ENDPOINT)] = completed(
        "context.dev", SCRAPE_ENDPOINT, markdown_payload(markdown), run_id="scrape-run"
    )
    server = create_server(FinanceService(client))

    result = await server.call_tool(
        "get_filing_items", {"ticker": "AAPL", "filing_type": "10-Q", "quarter": 2}
    )
    payload = cast(JsonObject, _result(result))

    assert isinstance(payload, dict)
    assert payload["year"] == 2025
    assert payload["filing_type"] == "10-Q"


def test_fd_tool_doc_contains_exactly_twenty_seven_tools() -> None:
    assert len(FD_TOOLS) == 27
