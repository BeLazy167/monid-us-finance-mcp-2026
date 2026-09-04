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

## The Kill: Before vs. After

| Metric | Financial Datasets API | Monid US Finance MCP |
|---|---|---|
| **Pricing Model** | $200/mo ($2,000/yr) or $20 per 1k calls | $0.0006 - $0.0009 per live call |
| **Commitment** | Monthly / Annual contract | Pay-per-query, zero subscription |
| **Cost for 22 Live Calls** | $0.4400 (Starter) / $0.0440 (Build) | **$0.0135 (Actual measured)** |
| **Savings** | Base | **96.9% cheaper** |
| **Failed Runs Handling** | Often billed / opaque | Auditable receipt in `receipts/ledger.jsonl` |

## Coverage

### MCP tools: 27 of 27 implemented

Every tool in `go/mcpserver/tool_schemas.json` runs against a live Monid route and is
contract-tested; see the `toolHandlers` table in `go/service/tools.go`. The advertised
tool names, descriptions and input schemas are diffed against the captured Financial
Datasets surface by test, so the two cannot silently drift.

### REST routes: 44 implemented, 2 zero-cost stubs

Two routes are registered for parity but answer `{"error": "not_implemented"}` at HTTP
200, with no Monid call and no charge:

- `/kpi/metrics/sectors` — sector is not a dimension the shared ticker catalog carries,
  so there is nothing honest to enumerate.
- `/index-funds/tickers` — `get_index_fund` resolves holdings by live web search per
  ticker; publishing the search-ranking hint list as a coverage catalog would overstate
  what this server supports.

Eight Financial Datasets routes are not registered at all. The four `as-reported`
statement routes need an XBRL/as-filed source that no allowlisted Monid provider
offers; `/ipos` needs an SEC S-1 filings feed, `/company/facts/ciks` and `/filings/ciks`
need CIK enumeration, and `/filings/items/requests/{request_id}` needs an async request
store. None are faked.

Route-by-route parameter and field notes, including every deliberate deviation, are in
[docs/openapi-notes.md](docs/openapi-notes.md) and [docs/compatibility.md](docs/compatibility.md).

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
