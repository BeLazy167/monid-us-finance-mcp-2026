from __future__ import annotations

import pytest

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
        "get_stock_prices",
        "get_stock_price",
        "get_news",
    }
