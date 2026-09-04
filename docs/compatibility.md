# Compatibility target

This server mirrors the Financial Datasets API **public interface**: the same 27 MCP tool names, the same input parameters, and response objects whose keys and key order match the Financial Datasets OpenAPI schemas in `docs/fd-contract-reference.json`. It is an independent, Monid-backed implementation; it is not affiliated with or endorsed by Financial Datasets, and no Financial Datasets data or outputs are used.

## Response contract

- Success responses contain only Financial Datasets schema keys, in schema property order. Unsourced optional fields are omitted rather than nulled or fabricated.
- All failures return the Financial Datasets `ErrorResponse` shape `{"error": <code>, "message": <detail>}`.
- Error codes in use: `bad_request`, `not_found`, `invalid_cursor`, `upstream_error`, `timeout`, `schema_drift`, `not_implemented`.
- Pagination matches the Financial Datasets style: `limit` is the total record budget across pages, `cursor` is an opaque base64url token, and `next_page_url` is present only when more records remain. Pages hold 10 records (100 for prices). `next_page_url` targets the documented facade base `https://api.monid-finance-mcp.example`; MCP clients should pass `cursor` back instead.
- Provenance, measured cost, and warnings never appear inside responses. They are committed to `receipts/ledger.jsonl` per Monid call (success or failure); `scripts/receipts_summary.py` and `summarize_ledger()` aggregate them.

## Tool status

Working tools (live Monid routes, contract-tested): `get_company_facts`, `get_income_statement`, `get_balance_sheet`, `get_cash_flow_statement`, `get_financial_metrics_snapshot`, `get_filings`, `get_stock_prices`, `get_stock_price`, `get_news`, `get_filing_items`, `list_filing_item_types`.

The remaining 16 tools are registered with their Financial Datasets parameters and answer `{"error": "not_implemented", ...}` at zero cost until their route and contract tests land.

| Dataset | MCP tool | Phase 1 path |
|---|---|---|
| Beneficial ownership | `get_beneficial_owners` | SEC 13D/13G aggregation |
| Beneficial ownership | `get_beneficial_ownership` | SEC 13D/13G aggregation |
| Company | `get_company_facts` | US company catalog (name, ticker) |
| Earnings | `get_earnings` | filings plus statements composition |
| Financial metrics | `get_financial_metrics` | statement and price-derived history |
| Financial metrics | `get_financial_metrics_snapshot` | live market summary |
| Financial statements | `get_income_statement` | normalized annual/quarterly/TTM statements |
| Financial statements | `get_balance_sheet` | normalized annual/quarterly/TTM statements |
| Financial statements | `get_cash_flow_statement` | normalized annual/quarterly/TTM statements |
| Index funds | `get_index_fund` | issuer or filing-derived holdings |
| Insider ownership | `get_insider_ownership` | SEC ownership statements where available |
| Insider trades | `get_insider_trades` | SEC Form 4 transactions |
| Institutional holdings | `get_institutional_investors` | SEC 13F filer discovery |
| Institutional holdings | `get_institutional_holdings` | SEC 13F positions |
| Interest rates | `get_interest_rates` | live central-bank snapshot from official sources |
| Operating KPIs | `get_kpi_guidance` | filing-derived requested KPI guidance |
| Operating KPIs | `get_kpi_metrics` | filing-derived requested KPI history |
| Operating KPIs | `get_kpi_non_gaap` | filing-derived non-GAAP metrics |
| News | `get_news` | entity-matched live news |
| SEC filings | `get_filings` | live SEC filing index |
| SEC filings | `get_filing_items` | filing section extraction |
| SEC filings | `list_filing_item_types` | deterministic supported item catalog |
| Segmented financials | `get_segmented_financials` | filing-derived product and geographic segments |
| Stock prices | `get_stock_prices` | historical OHLCV with local interval aggregation |
| Stock prices | `get_stock_price` | latest price snapshot |
| Search | `screen_stocks` | financial and company filters |
| Search | `list_stock_screener_filters` | deterministic filter catalog |

A tool becomes working only after a live endpoint probe and a contract test. Until then it returns `not_implemented` rather than fabricated data.

## Honest deviations from Financial Datasets behavior

- `cik` parameters are accepted (contract parity) but answered with `bad_request`; only ticker lookup is routed. Company facts currently sources ticker and name only.
- `get_news` requires `ticker`; market-wide news is not routed.
- Statement `filing_date*` filters need the filings join; if that join fails with filters active, the call answers `upstream_error` instead of guessing.
- TTM statements derive locally from four consecutive quarters (flows summed, weighted-average shares averaged, balances point-in-time). TTM records omit `fiscal_period` and `currency`.
- `get_stock_prices` returns ascending time order and aggregates day bars into week/month/year locally; `time` is the bar end date in UTC.
- `get_filing_items` accepts `include_exhibits` for parity but answers `bad_request` when set: exhibits are not sourced.
- `get_filings` validates `filing_type` against the Financial Datasets enum (10-K, 10-Q, 8-K, 20-F, 6-K).
- `list_filing_item_types` reuses each item's SEC title as `description`.
- Records omit fields the validated routes cannot source (for example CIK, currency, EBITDA on income statements); omitted keys are schema-legal.
