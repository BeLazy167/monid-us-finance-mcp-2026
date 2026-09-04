# Live smoke evidence

The first US slice was smoke-tested on 2026-09-04 after the unit suite passed. The test used `AAPL` through `FinanceService` and the production `MonidClient`. The client inspected each endpoint immediately before its paid run.

| Tool | Monid endpoint | Run ID | Measured cost | Result |
|---|---|---|---:|---|
| `get_company_facts` | DefiLlama `/equities/v1/companies-list` | `01M1N5KCJ7ZA33MC0EYK7Y4AY2` | $0.0006 | completed, HTTP 200 |
| `get_company_facts` | DefiLlama `/equities/v1/summary` | `01M1N5KG83AP0JBB2583EXEWG8` | $0.0006 | completed, HTTP 200 |
| `get_income_statement` | DefiLlama `/equities/v1/statements` | `01M1N4KBRQ47SQ3PYH7R36CT03` | $0.0006 | completed, HTTP 200 |
| `get_balance_sheet` | DefiLlama `/equities/v1/statements` | `01M1N4KEFVYT8SC7F23NJDPNSH` | $0.0006 | completed, HTTP 200 |
| `get_cash_flow_statement` | DefiLlama `/equities/v1/statements` | `01M1N4KGN4K8AZ1TPP7VSJ9S4M` | $0.0006 | completed, HTTP 200 |
| `get_financial_metrics_snapshot` | DefiLlama `/equities/v1/summary` | `01M1N4KJV6M720NFPS357SDPKS` | $0.0006 | completed, HTTP 200 |
| `get_filings` | DefiLlama `/equities/v1/filings` | `01M1N4KMX9BTDNZ1E41MN0BXRC` | $0.0006 | completed, HTTP 200 |
| `get_filing_items` index | DefiLlama `/equities/v1/filings` | `01M1N6VGY1679HRHZMZK1WSQZ8` | $0.0006 | completed, HTTP 200 |
| `get_filing_items` scrape | Context.dev `/web/scrape/markdown` | `01M1N6VK4CXRHBGED8YGAV5ADH` | $0.0009 | completed, HTTP 200 |
| `get_stock_prices` | DefiLlama `/equities/v1/ohlcv` | `01M1N4KQSBVSKT3JN0BB6NW4SX` | $0.0006 | completed, HTTP 200 |
| `get_stock_price` | DefiLlama `/equities/v1/summary` | `01M1N4KVSFFW0N0CFEFXABBPD7` | $0.0006 | completed, HTTP 200 |
| `get_news` | Context.dev `/news/search` | `01M1N4KXVWWNTWGQM5Y8VW5FJE` | $0.0009 | completed, HTTP 200 |

The current evidence rows cost **$0.0078**. An earlier company-facts probe used runs `01M1N4K8A2W5418F5E0SYEGV36` and `01M1N4K89YPZVV0RW30WRZ9ZYZ`. It cost $0.0012 before the adjacency lock changed. Total live validation spend was **$0.0090**.

The Gate A smoke ran only after the unit suite passed. It selected Apple accession `0000320193-25-000079` for the 2025 10-K and extracted one `Item-1` body section with 16,053 characters. Both runs completed without warnings or partial errors. The Gate A smoke cost **$0.0015**, below its $0.01 budget.

The smoke confirmed company catalog hydration, statement matrices, SEC filing URLs, local filing body extraction, OHLCV filtering, summary metrics, and entity-matched news. It did not test every ticker, exchange calendar, corporate currency, provider outage, or redistribution right.


## FD-contract full pass — 2026-09-04 (commit `90b3458` + fixes)

Second full pass after the Financial Datasets contract refactor, run through `scripts/live_smoke.py` with the production `MonidClient`. `monid inspect` was failing registry-side (HTML error) during this window, so the client exercised its documented fallback: every `(provider, endpoint)` pair was checked against the committed discovery allowlist `docs/monid_finance_discovery.json` before the paid run. All 12 calls completed with HTTP 200; measured cost $0.0078 (cumulative ledger $0.0135 over both passes). One earlier news call failed with provider HTTP 400 (missing body) and is recorded as a failure receipt; the fix landed before this pass.

| Tool | Monid endpoint | Run ID | Measured cost | Result |
|---|---|---|---:|---|
| `get_company_facts` | DefiLlama `/equities/v1/companies-list` | `01M1NCJ35ZTHKYYKWYHE54ZB5P` | $0.0006 | completed, HTTP 200 |
| `get_income_statement` | DefiLlama `/equities/v1/statements` | `01M1NCJ7F3XKNH273WRR8V37QE` | $0.0006 | completed, HTTP 200 |
| `get_income_statement` | DefiLlama `/equities/v1/filings` | `01M1NCJ9Z1D76DQT87AM3AB684` | $0.0006 | completed, HTTP 200 (identity join) |
| `get_cash_flow_statement` | DefiLlama `/equities/v1/statements` | `01M1NCJCB8SAPKGNG80KYP4JK6` | $0.0006 | completed, HTTP 200 |
| `get_cash_flow_statement` | DefiLlama `/equities/v1/filings` | `01M1NCJF9TASKF42SJJ2RZ1HK4` | $0.0006 | completed, HTTP 200 (identity join) |
| `get_financial_metrics_snapshot` | DefiLlama `/equities/v1/summary` | `01M1NCJHRP8XV3FVN8PW3YN5GC` | $0.0006 | completed, HTTP 200 |
| `get_stock_price` | DefiLlama `/equities/v1/summary` | `01M1NCJM1ZY89XZ2W5TRBH370S` | $0.0006 | completed, HTTP 200 |
| `get_stock_prices` | DefiLlama `/equities/v1/ohlcv` | `01M1NCJPG93XBV1ZP6RPDSHH1F` | $0.0006 | completed, HTTP 200 |
| `get_filings` | DefiLlama `/equities/v1/filings` | `01M1NCJT154ZC29N5S5HDE549G` | $0.0006 | completed, HTTP 200 |
| `get_news` | Context.dev `/news/search` | `01M1NCJWC19E8KCAFT3Y8H8037` | $0.0009 | completed, HTTP 200 |
| `get_filing_items` | DefiLlama `/equities/v1/filings` | `01M1NCK0VNVHPSKJRHX9AR7ETY` | $0.0006 | completed, HTTP 200 |
| `get_filing_items` | Context.dev `/web/scrape/markdown` | `01M1NCK35M70PF31T2F791KVQG` | $0.0009 | completed, HTTP 200 |

Verified live behaviors: Apple FY2025 income statement ($416.161B revenue, EPS 7.49) with fiscal-period labels and schema key order; TTM cash flow derived from four consecutive quarters; monthly price bars aggregated locally with integer volumes; margins as ratios; AAPL news with title/date/url; Item-1A risk factors extracted from the FY2024 10-K (`0000320193-24-000123`).
