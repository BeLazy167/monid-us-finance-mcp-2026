"""Print an auditable cost comparison table from the committed receipts ledger.

Compares measured live Monid call costs against Financial Datasets API pricing:
- Starter / Pay-as-you-go: $20.00 per 1,000 calls ($0.0200 / req)
- Build Plan: $200.00 / month for 100,000 calls ($0.0020 / req)
"""
from __future__ import annotations

import os
from pathlib import Path

from monid_finance_mcp.receipts import summarize_ledger

FD_PAY_PER_CALL = 0.0200
FD_BUILD_PER_CALL = 0.0020


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

    fd_pay_cost = total_calls * FD_PAY_PER_CALL
    fd_build_cost = total_calls * FD_BUILD_PER_CALL

    print("=" * 78)
    print("  MONID vs. FINANCIAL DATASETS API — MEASURED COST RECEIPT")
    print("=" * 78)
    print("  Target product killed : Financial Datasets API (financialdatasets.ai)")
    print("  Target plan / price   : Build Plan $200/mo ($2,000/yr) | Starter $20/1k calls")
    print("  Replacement server    : Monid US Finance MCP (live multi-provider routing)")
    print(f"  Total Monid calls     : {total_calls} (including {failures} failed runs)")
    print(f"  Measured Monid spend  : ${total_cost:.4f} USD")
    print(f"  Average cost per call : ${avg_cost:.6f} USD")
    print("-" * 78)
    print(f"  {'Cost for this run':<30} | {'Spend':<12} | {'Savings vs FD':<18}")
    print("-" * 78)
    print(f"  Financial Datasets Starter     | ${fd_pay_cost:>8.4f} USD | base")
    print(f"  Financial Datasets Build Plan  | ${fd_build_cost:>8.4f} USD | base")
    savings_starter = (1 - (total_cost / fd_pay_cost)) * 100 if fd_pay_cost else 0
    savings_label = f"{savings_starter:>6.1f}% cheaper"
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
