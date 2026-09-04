# Monid Finance MCP

A US-first financial-data MCP server for agents. It mirrors the published Financial Datasets MCP workflows while paying per live Monid call instead of requiring a monthly seat.

Status: phase 1 implementation. Do not use this software as investment advice.

## Phase 1

The target contract is the 27 tools published at `docs.financialdatasets.ai/mcp-server`. Two documented dataset groups, activist ownership and IPOs, are included as extensions.

The first implemented US slice includes:

- `get_company_facts`;
- `get_income_statement`, `get_balance_sheet`, and `get_cash_flow_statement`;
- `get_financial_metrics_snapshot`;
- `get_filings`;
- `get_filing_items` and `list_filing_item_types`;
- `get_stock_prices` and `get_stock_price`;
- `get_news`.

Each tool returns one JSON envelope with `data`, `provenance`, `total_cost`, `warnings`, and `partial_errors`.

Every successful response reports:

- source provider and endpoint;
- source freshness when available;
- Monid run ID;
- measured Monid cost;
- partial failures and unsupported fields.

No tool returns mock market data.

This slice does not claim full parity. DefiLlama equity data is beta. Price data is labeled indicative or delayed/EOD. TTM statements are not derived. Context.dev news currently requires a ticker.

`get_filing_items` selects a filing by SEC report year, optional report quarter, and optional accession number. It accepts `10-K`, `10-Q`, and `8-K`. Filing years must be between 1994 and next calendar year. Item names include `Item-1A`, `Item-7`, and `Item-1.01`. The 10-Q catalog uses explicit names such as `Part-I-Item-1` and `Part-II-Item-1` because item numbers repeat. It also accepts public aliases such as `Part-1,Item-1`. `include_exhibits=true` returns a free `capability_unavailable` error.

The tool pays for a DefiLlama filing-index lookup, validates the selected `https://www.sec.gov/Archives/` URL, then pays for a Context.dev markdown scrape. A local parser skips table-of-contents headings and extracts body sections without an LLM. `list_filing_item_types` reads a versioned static catalog sourced from the SEC form instructions. It makes no Monid call and does not claim upstream catalog support.

## Setup

Requirements: Python 3.12+, `uv`, Node.js, and Monid CLI 0.1.7 or newer.

```bash
npm install -g @monid-ai/cli@latest
monid setup
monid keys add -k YOUR_MONID_KEY -l main
uv sync
uv run monid-finance-mcp
```

Configure an MCP client to run `uv --directory /absolute/path/to/monid-us-finance-mcp-2026 run monid-finance-mcp` over stdio.

## Verify

```bash
uv run ruff check .
uv run pyright
uv run pytest
```

See [compatibility](docs/compatibility.md), [provenance](docs/provenance.md), [live smoke evidence](docs/live-smoke.md), and [phase 2](docs/phase-2-india.md).
