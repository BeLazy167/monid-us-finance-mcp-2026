# Financial Datasets API vs. this server — measured comparison

Every number below is empirical, not estimated. Financial Datasets (FD) figures come from live calls made 2026-09-04 against `api.financialdatasets.ai` and `mcp.financialdatasets.ai` with a real API key, plus their published docs. Our figures come from the committed receipts ledger (`receipts/ledger.jsonl`) over the recorded live passes.

## Price per request

FD publishes per-request prices (docs.financialdatasets.ai). Our cost is the measured Monid spend for the same tool call, including every supporting call and failed run.

| Request | FD price | Our measured cost | Multiple |
|---|---:|---:|---:|
| Income statement (ticker, period, limit) | $0.04 | $0.0012 (statements + filings join) | 33x |
| Balance sheet | $0.04 | $0.0012 | 33x |
| Cash flow statement | $0.04 | $0.0012 | 33x |
| Financial metrics (historical) | $0.04 | $0.0012 | 33x |
| Financial metrics snapshot | $0.04 | $0.0006 | 67x |
| Segmented financials | $0.04 | $0.0096 (filings + fact-checked extract) | 4.2x |
| Earnings (ticker) | $0.01 | $0.0012 (statements + filings composition) | 8.3x |
| SEC filings | $0.02 | $0.0006 | 33x |
| Filing items (e.g. Item 1A) | – | $0.0015 (filings + scrape) | – |
| Stock prices (range) | $0.02 | $0.0006 | 33x |
| Price snapshot | $0.02 | $0.0006 | 33x |
| News | $0.04 | $0.0009 | 44x |
| Insider trades | $0.04 | $0.01 (SECForm4 search) | 4x |
| Stock screener | $0.01 / 10 filters | $0.01 (Nasdaq screener) | 1x |
| Interest rates | $0.02 | $0.0036 (4 central-bank scrapes) | 5.6x |

FD plans: Developer $200/mo ($2,000/yr), Pro $2,000/mo ($20,000/yr). A month of daily one-shot research on 20 tickers (statements + metrics + filings + prices each) costs $200 on the Developer plan, $3.20 pay-per-request on FD credits, and **$1.44** here.

## Response parity (verified live, same inputs)

- **Income statements** (`/financials/income-statements?ticker=AAPL&period=annual`): identical key set and key order; AAPL FY2025 revenue 416,161,000,000 on both sides; same accession number (0000320193-25-000079), form type, filing date (2025-10-31), and URL.
- **fiscal_period** format: `FY2025` (annual) and `2026-Q3` (quarterly) — matched.
- **Change fields**: FD `*_chg` / `*_yoy_chg` are decimal ratios (0.10 = +10%); ours compute the same semantics, `yoy` fields quarterly-only.
- **Filings**: same record shape (`accession_number`, `filing_type`, `report_date`, `filing_date`, `ticker`, `url`); same 10-K set returned.
- **Prices**: same `Price` shape; day bars in ascending time order.
- **Earnings**: same `EarningsRecord` shape incl. `quarterly` block and ratio `yoy_chg` fields. FD's freshest record is the 8-K press release (filed 2026-07-30); ours composes the 10-Q (filed 2026-07-31) — same quarter, same schema, one day later source.
- **MCP**: 27 tools with identical names, parameter names/order/required flags, and verbatim descriptions (captured live into `docs/fd-mcp-tools.json`). List tools return the bare records array; object tools the bare object; errors `{"error", "message"}`.
- **REST**: same routes, same wrapped responses (`{"income_statements": [...]}` + `next_page_url` cursor pagination), same `X-API-KEY` header auth.
- **Depth parity, honestly measured**: FD's live MCP returned 3 annual income statements for AAPL at `limit=25`; our DefiLlama matrix carries the same recent-years depth. Neither serves 30 years on this endpoint in practice today.

## Coverage gaps (what FD has that we do not)

- Ticker universe: FD 27,000+ US tickers, 30+ years; ours is DefiLlama US equities (~3,227 companies) + SEC EDGAR filings.
- Earnings records from 8-K press releases with consensus estimates, surprise flags, and `signals` arrays; ours composes 10-K/10-Q statements (feed mode adds the calendar route).
- Minute-level price bars (`interval_multiplier`), market-wide news, CIK lookups. As-reported line items are served from EDGAR's rendered statement files, matching Financial Datasets' structure but printing the filing's own labels.
- Ownership-state tools (`get_beneficial_owners`, `get_beneficial_ownership`, `get_insider_ownership`, `get_institutional_investors`): answered with honest typed errors — the validated feeds are capped/stale, and current-state ownership cannot be reduced safely from them.
- Insider trades capped at the validated 15-row SECForm4 feed; screener executes `exchange` and `market_cap` filters only.
- No SLAs, no webhooks, no real-time tick feeds.

## What this is not

This is an independent, Monid-backed implementation that mirrors FD's public interface. No FD data or outputs are used as a source; all values come from Monid providers (SEC EDGAR via filings, DefiLlama, Context.dev, SECForm4, Nasdaq). FD is a trademark of Financial Datasets, Inc.; this project is not affiliated with or endorsed by them.
