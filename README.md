# Monid Finance MCP

A US-first financial-data MCP server for agents. It mirrors the published Financial Datasets MCP workflows while paying per live Monid call instead of requiring a monthly seat.

Status: phase 1 implementation. Do not use this software as investment advice.

## Phase 1

The target contract is the 27 tools published at `docs.financialdatasets.ai/mcp-server`. Two documented dataset groups, activist ownership and IPOs, are included as extensions.

Every successful response reports:

- source provider and endpoint;
- source freshness when available;
- Monid run ID;
- measured Monid cost;
- partial failures and unsupported fields.

No tool returns mock market data.

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

See [compatibility](docs/compatibility.md), [provenance](docs/provenance.md), and [phase 2](docs/phase-2-india.md).
