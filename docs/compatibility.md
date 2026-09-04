# Compatibility target

"Compatible" means the same tool name and user job. Output adds provenance and cost. It does not mean byte-for-byte proprietary responses.

| Dataset | MCP tool | Phase 1 path |
|---|---|---|
| Beneficial ownership | `get_beneficial_owners` | SEC 13D/13G aggregation |
| Beneficial ownership | `get_beneficial_ownership` | SEC 13D/13G aggregation |
| Company | `get_company_facts` | US company catalog plus live market summary |
| Earnings | `get_earnings` | live earnings history and calendar |
| Financial metrics | `get_financial_metrics` | statement and price-derived history |
| Financial metrics | `get_financial_metrics_snapshot` | live market summary |
| Financial statements | `get_income_statement` | normalized annual or quarterly statements |
| Financial statements | `get_balance_sheet` | normalized annual or quarterly statements |
| Financial statements | `get_cash_flow_statement` | normalized annual or quarterly statements |
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
| Stock prices | `get_stock_prices` | historical OHLCV |
| Stock prices | `get_stock_price` | latest price snapshot |
| Search | `screen_stocks` | financial and company filters |
| Search | `list_stock_screener_filters` | deterministic filter catalog |
| Activist ownership | `get_activist_ownership` | SEC Schedule 13D extension |
| IPOs | `get_ipos` | live US IPO calendar extension |

A row becomes supported only after a live endpoint probe and a contract test. Until then, the tool returns a typed capability error rather than fabricated data.
