from __future__ import annotations

from typing import cast

import pytest

from monid_finance_mcp.models import JsonObject
from monid_finance_mcp.server import create_server
from monid_finance_mcp.service import FinanceService

from .test_service import full_client


@pytest.mark.asyncio
async def test_server_registers_assigned_published_tool_names() -> None:
    server = create_server(FinanceService(full_client()))

    tools = await server.list_tools()

    assert {tool.name for tool in tools} == {
        "get_company_facts",
        "get_income_statement",
        "get_balance_sheet",
        "get_cash_flow_statement",
        "get_financial_metrics_snapshot",
        "get_filings",
        "get_filing_items",
        "list_filing_item_types",
        "get_stock_prices",
        "get_stock_price",
        "get_news",
    }


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
