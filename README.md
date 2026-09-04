# Monid US Finance MCP (Financial Datasets API Alternative)

**Live API:** https://monid-finance-api.fly.dev
**Docs site:** https://monid-us-finance-mcp-2026.vercel.app
**MCP endpoint:** `https://monid-finance-api.fly.dev/mcp` (also served at `/api`)

Bring your own Monid API key: send it as `X-API-KEY` and every upstream call bills
your own Monid wallet. We never store or log keys.

```bash
curl -H "X-API-KEY: monid_live_..." \
  "https://monid-finance-api.fly.dev/financials/income-statements?ticker=AAPL&period=annual&limit=1"
```

An agent-native, US-first financial data MCP server built for the September 2026 Monid Hackathon.

It replaces the paid research layer of **Financial Datasets API** (financialdatasets.ai — $200/month Build Plan or $20/1k credits) with live, pay-per-call routing across SEC EDGAR, DefiLlama US equities, and Context.dev via Monid.

## Key Properties

- **100% Contract Parity**: Implements the identical 27 MCP tool names, input parameters, and response schemas as Financial Datasets API. Responses contain only official schema keys in OpenAPI property order.
- **Auditable Measured Receipts**: Provenance, costs, and run IDs are committed to `receipts/ledger.jsonl`. A 22-call comprehensive live test cost **$0.0135 USD** (96.9% cheaper than incumbent credit packs).
- **Zero Mock Data**: Never fabricates numbers. If a field or tool cannot be honestly sourced, it is omitted or returns a zero-cost typed error.
- **Deterministic SEC Parsing**: `get_filing_items` extracts canonical 10-K/10-Q/8-K sections using rule-based parsing with zero LLM hallucination risk.

## Quickstart

```bash
# Clone and sync dependencies
git clone https://github.com/belazy/monid-us-finance-mcp-2026.git
cd monid-us-finance-mcp-2026
uv sync

# Run tests (100% pass, pyright strict, ruff compliant)
uv run pytest
uv run pyright
uv run ruff check .

# Run the live smoke workflow
uv run python scripts/live_smoke.py

# Inspect the cost comparison receipt
uv run python scripts/receipts_summary.py
```

## The Kill: Before vs. After

| Metric | Financial Datasets API | Monid US Finance MCP |
|---|---|---|
| **Pricing Model** | $200/mo ($2,000/yr) or $20 per 1k calls | $0.0006 - $0.0009 per live call |
| **Commitment** | Monthly / Annual contract | Pay-per-query, zero subscription |
| **Cost for 22 Live Calls** | $0.4400 (Starter) / $0.0440 (Build) | **$0.0135 (Actual measured)** |
| **Savings** | Base | **96.9% cheaper** |
| **Failed Runs Handling** | Often billed / opaque | Auditable receipt in `receipts/ledger.jsonl` |

## Tool Coverage (27 Tools)

### Live Working Tools (11)
- `get_company_facts`: Basic company profile and name.
- `get_income_statement`: Annual, quarterly, and derived TTM income statements.
- `get_balance_sheet`: Annual, quarterly, and point-in-time balance sheets.
- `get_cash_flow_statement`: Annual, quarterly, and derived TTM cash flows.
- `get_financial_metrics_snapshot`: Valuation multiples, margins, and ratios.
- `get_filings`: SEC filings index with form-type filtering.
- `get_filing_items`: Deterministic section extraction (e.g. Item 1A Risk Factors).
- `list_filing_item_types`: SEC catalog item descriptions.
- `get_stock_price`: Latest quote snapshot with 1-day change.
- `get_stock_prices`: Historical OHLCV with daily, weekly, monthly aggregation.
- `get_news`: Entity-matched news articles with dates and URLs.

### Honest Stubs (16)
The remaining 16 tools (`get_earnings`, `get_financial_metrics`, `get_insider_trades`, `screen_stocks`, etc.) are fully registered with their Financial Datasets parameters and return a clean, typed `{"error": "not_implemented", ...}` response at **zero cost**.

## Honest Scope & Limitations

1. **Not a Trading Terminal**: Does not provide real-time tick websocket feeds or sub-second pricing.
2. **Normalized Depth**: Sourced from DefiLlama US equities beta and SEC EDGAR; does not claim proprietary 30-year normalized history.
3. **No SLAs**: Distributed multi-provider routing without enterprise SLAs.

## Documentation

- [Compatibility & Schema Target](docs/compatibility.md)
- [Architecture & Design Boundaries](docs/architecture.md)
- [Live Smoke & Run Receipts](docs/live-smoke.md)
- [Demo & Submission Kit](docs/DEMO_AND_SUBMISSION_KIT.md)

## License

MIT License. See [LICENSE](LICENSE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
