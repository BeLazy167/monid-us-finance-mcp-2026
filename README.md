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

It replaces the paid research layer of **Financial Datasets API** (financialdatasets.ai — Build at $200/month with 100,000 requests included and $10 per additional 1,000) with live, pay-per-call routing across SEC EDGAR, DefiLlama US equities, and Context.dev via Monid.

## Key Properties

- **100% Contract Parity**: Implements the identical 27 MCP tool names, input parameters, and response schemas as Financial Datasets API. Responses contain only official schema keys in OpenAPI property order.
- **Auditable Measured Receipts**: Provenance, costs, and run IDs are committed to `receipts/ledger.jsonl`. 51 priced live calls total **$0.0515 USD**, a mean of **$0.00101 per call**. Every figure below is read off that ledger, not estimated.
- **Zero Mock Data**: Never fabricates numbers. If a field or tool cannot be honestly sourced, it is omitted or returns a zero-cost typed error.
- **Deterministic SEC Parsing**: `get_filing_items` extracts canonical 10-K/10-Q/8-K sections using rule-based parsing with zero LLM hallucination risk.

## Quickstart

The server is a single Go binary. There is no Python, Node, CLI, or proxy hop.

```bash
git clone https://github.com/belazy/monid-us-finance-mcp-2026.git
cd monid-us-finance-mcp-2026/go

go build ./...
go vet ./...
go test ./... -race
```

Run it locally, then call it with your own Monid key:

```bash
go run ./cmd/server            # listens on :8080, serves REST, /mcp and the static site

curl -H "X-API-KEY: monid_live_..." \
  "http://localhost:8080/financials/income-statements?ticker=AAPL&period=annual&limit=1"
```

Every Monid call is appended to a receipts ledger when `RECEIPTS_PATH` is set; the
ledger is best-effort observability and never a response dependency.

## Cost: measured, with the caveat stated

Financial Datasets prices by request count, not by dataset. Both paid plans work out
to the same effective rate on included volume; the tiers buy volume and rights
(redistribution, webhooks, uptime SLAs), not a cheaper unit.

| | Financial Datasets | Monid US Finance MCP |
|---|---|---|
| **Entry cost** | $200/month, before the first request | $0.0006, no subscription |
| **Build** | $200/mo, 100,000 requests included, $10 per additional 1,000 | — |
| **Scale** | $2,000/mo, 1,000,000 requests included, $5 per additional 1,000 | — |
| **Effective rate, included volume** | $0.00200 / request (both plans) | — |
| **Overage rate** | $0.01000 (Build) / $0.00500 (Scale) | — |
| **Measured rate** | — | **$0.00101 mean, $0.00060 median** |
| **Commitment** | Monthly subscription | Pay-per-query, none |
| **Failed runs** | Billing not itemized per call | Failure receipt written to `receipts/ledger.jsonl` |

Measured distribution across 51 priced calls: **82% at $0.0006**, 14% at $0.0009,
4% at $0.0100. The two $0.0100 calls were `nasdaq/get_stock_screener` and
`secform4/search`.

**Where we win.** The floor. Financial Datasets costs $200/month before the first
request; the full 51-call ledger here cost $0.0515. At our measured mean we are about
half the $0.002 included rate, and roughly 90% below the $0.010 Build overage rate.

**Where we do not.** Our cost is per-route and variable, not flat. A workload made
entirely of our $0.0100 routes costs 5x the included rate: 100,000 such calls would run
about $1,000 here against $200 on Build. High-volume, extraction-heavy usage is cheaper
on a Financial Datasets subscription, and this table is not an argument otherwise.

We also do not offer what the paid tiers include: data redistribution rights, webhooks,
uptime SLAs, zero data retention, bulk delivery, or 30+ years of normalized history.

## Coverage

### MCP tools: 27 of 27 implemented

Every tool in `go/mcpserver/tool_schemas.json` runs against a live Monid route and is
contract-tested; see the `toolHandlers` table in `go/service/tools.go`. The advertised
tool names, descriptions and input schemas are diffed against the captured Financial
Datasets surface by test, so the two cannot silently drift.

### REST routes: all 54, with 2 honest stubs

Every one of Financial Datasets' 54 REST paths is registered. 52 return data.
Two answer
`{"error": "not_implemented"}` at HTTP 200, with no Monid call and no charge:

- `/kpi/metrics/sectors` — sector is not a dimension the shared ticker catalog
  carries, so there is nothing honest to enumerate.
- `/index-funds/tickers` — `get_index_fund` resolves holdings by live web search
  per ticker; publishing the search-ranking hint list as a coverage catalog
  would overstate what this server supports.

The four `as-reported` statement routes read the rendered statement files EDGAR
generates from a filing's own XBRL presentation linkbase, so the `line_items`
tree is the filing's hierarchy. They match Financial Datasets' structure, not
its labels: this server prints the label the filing prints.

Several registered routes deviate deliberately, each forced by its source and
each documented: `/ipos` and `/institutional-holdings/investors` require a
ticker, `/company/facts/ciks` covers 8,005 CIKs against Financial Datasets'
21,005, and `/macro/interest-rates/banks` lists the four central banks this
server actually scrapes rather than ten. Route-by-route notes are in
[docs/openapi-notes.md](docs/openapi-notes.md) and
[docs/compatibility.md](docs/compatibility.md).

### A known upstream data defect

Measured 2026-09-04 against Apple's FY2025 10-K: the normalized statements feed
behind `/financials/cash-flow-statements` reports investing activities as
27,910,000,000 where the filing says 15,195,000,000. The gap is exactly Apple's
12,715,000,000 capital expenditure, which that feed omits from the investing
subtotal while still using it to compute Free Cash Flow correctly. The error
carries into `change_in_cash_and_equivalents`. Operating and financing match the
filing exactly.

This is an upstream aggregation defect, not a parsing bug here, and it will be
wrong for any company with material capex. It is written down rather than
quietly carried.

### Data freshness is measured, not assumed

Feeds age differently and the docs say so per route. Measured live on 2026-09-04: the
13D/13G beneficial-ownership feeds ran roughly six months behind, insider trading
tracked filings within days, and the nasdaq market snapshot carried its own as-of
timestamp. Every row carries its own sourced dates; nothing stale is described as
current.

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
