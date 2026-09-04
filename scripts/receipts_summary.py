"""Print an auditable cost comparison table from the committed receipts ledger.

Compares measured live Monid call costs against Financial Datasets API pricing:
- Starter / Pay-as-you-go: $20.00 per 1,000 calls ($0.0200 / req)
- Build Plan: $200.00 / month for 100,000 calls ($0.0020 / req)
"""
from __future__ import annotations

import os
from pathlib import Path

from monid_finance_mcp.receipts import summarize_ledger

# Financial Datasets published per-request prices (docs.financialdatasets.ai, 2026-09-04)
FD_PER_REQUEST_PRICES: dict[str, float] = {
    "get_income_statement": 0.04,
    "get_balance_sheet": 0.04,
    "get_cash_flow_statement": 0.04,
    "get_financial_metrics": 0.04,
    "get_financial_metrics_snapshot": 0.04,
    "get_segmented_financials": 0.04,
    "get_news": 0.04,
    "get_insider_trades": 0.04,
    "get_earnings": 0.01,
    "screen_stocks": 0.01,
    "get_stock_prices": 0.02,
    "get_stock_price": 0.02,
    "get_filings": 0.02,
    "get_interest_rates": 0.02,
}
FD_DEFAULT_PRICE = 0.04


def _as_int(value: object) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else 0


def _as_float(value: object) -> float:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return 0.0
    return float(value)


def main() -> None:
    ledger_path = Path(os.environ.get("MONID_LEDGER_PATH", "receipts/ledger.jsonl"))
    summary = summarize_ledger(ledger_path)

    total_calls = _as_int(summary.get("calls"))
    failures = _as_int(summary.get("failures"))
    total_cost = _as_float(summary.get("total_usd_cost"))
    avg_cost = total_cost / total_calls if total_calls else 0.0

    tools = summary.get("tools")
    fd_equivalent = 0.0
    if isinstance(tools, dict):
        for tool, stats in tools.items():
            if isinstance(stats, dict):
                calls = stats.get("calls", 0)
                fd_equivalent += FD_PER_REQUEST_PRICES.get(tool, FD_DEFAULT_PRICE) * (
                    calls if isinstance(calls, int) else 0
                )

    print("=" * 78)
    print("  MONID vs. FINANCIAL DATASETS API — MEASURED COST RECEIPT")
    print("=" * 78)
    print("  Target product killed : Financial Datasets API per-request layer")
    print("  Target plan / price   : Developer $200/mo | Pro $2,000/mo | $0.01-$0.04 per request")
    print("  Replacement server    : Monid US Finance MCP (live multi-provider routing)")
    print(f"  Total Monid calls     : {total_calls} (including {failures} failed runs)")
    print(f"  Measured Monid spend  : ${total_cost:.4f} USD")
    print(f"  Average cost per call : ${avg_cost:.6f} USD")
    print("-" * 78)
    print(f"  {'Cost for this run':<30} | {'Spend':<12} | {'Savings vs FD':<18}")
    print("-" * 78)
    print(f"  Financial Datasets pay-per-req | ${fd_equivalent:>8.4f} USD | base")
    savings = (1 - (total_cost / fd_equivalent)) * 100 if fd_equivalent else 0
    savings_label = f"{savings:>6.1f}% cheaper"
    print(f"  Monid US Finance MCP (ACTUAL)  | ${total_cost:>8.4f} USD | {savings_label}")
    print("-" * 78)
    print("  PER-TOOL MEASURED BREAKDOWN:")
    print(f"  {'Tool':<32} {'Calls':<8} {'Failures':<10} {'Measured USD':<12}")
    print("-" * 78)
    tools = summary.get("tools")
    if isinstance(tools, dict):
        for tool, stats in sorted(tools.items()):
            if isinstance(stats, dict):
                c = stats.get("calls", 0)
                f = stats.get("failures", 0)
                usd = _as_float(stats.get("usd_cost"))
                print(f"  {tool:<32} {c!s:<8} {f!s:<10} ${usd:.4f}")
    print("=" * 78)
    print("  Honest qualification:")
    print("  - Sourced from SEC EDGAR, DefiLlama US equities, Context.dev.")
    print("  - No tick-by-tick real-time websocket, 30-year normalized depth, or SLAs.")
    print("  - Zero cost on validation errors and unimplemented stubs.")
    print("=" * 78)


if __name__ == "__main__":
    main()
