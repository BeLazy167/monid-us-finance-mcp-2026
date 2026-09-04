"""Live end-to-end smoke test of the Financial Datasets contract tools.

Runs the working tools against live Monid endpoints, writes a receipts
ledger row for every call, and prints the FD-shaped responses plus the
measured cost. Budget: well under $0.03 per full pass.
"""
from __future__ import annotations

import asyncio
import json
import os
from pathlib import Path

from monid_finance_mcp.client import MonidClient
from monid_finance_mcp.receipts import ReceiptsLedger, summarize_ledger
from monid_finance_mcp.service import FinanceService


def show(name: str, response: object) -> None:
    print(f"\n=== {name} ===")
    print(json.dumps(response, indent=1, default=str)[:1500])


async def main() -> None:
    ledger = ReceiptsLedger(Path(os.environ.get("MONID_LEDGER_PATH", "receipts/ledger.jsonl")))
    allowlist = Path(__file__).resolve().parents[1] / "docs" / "monid_finance_discovery.json"
    client = MonidClient(
        cli=os.environ.get("MONID_CLI", "monid"),
        allowlist_path=allowlist,
    )
    service = FinanceService(client, ledger)

    show("get_company_facts", await service.get_company_facts(ticker="AAPL"))
    show(
        "get_income_statement(annual)",
        await service.get_income_statement(
            ticker="AAPL", period="annual", limit=2
        ),
    )
    show(
        "get_cash_flow_statement(ttm)",
        await service.get_cash_flow_statement(
            ticker="AAPL", period="ttm", limit=1
        ),
    )
    show(
        "get_financial_metrics_snapshot",
        await service.get_financial_metrics_snapshot(ticker="AAPL"),
    )
    show("get_stock_price", await service.get_stock_price("AAPL"))
    show(
        "get_stock_prices(month)",
        await service.get_stock_prices(
            ticker="AAPL", interval="month", start_date="2026-07-01", end_date="2026-08-31"
        ),
    )
    show("get_filings", await service.get_filings(ticker="AAPL", filing_type=["10-K"], limit=3))
    show("get_news", await service.get_news(ticker="AAPL", limit=3))
    show(
        "get_filing_items(Item-1A)",
        await service.get_filing_items(
            ticker="AAPL", filing_type="10-K", year=2024, item="Item-1A"
        ),
    )


    summary = summarize_ledger(ledger.path)
    print("\n=== receipts summary ===")
    print(json.dumps(summary, indent=1))
    ledger.close()


if __name__ == "__main__":
    asyncio.run(main())
