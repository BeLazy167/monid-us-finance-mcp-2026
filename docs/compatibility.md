# Compatibility target

This server mirrors the Financial Datasets API **public interface**: the same 27 MCP tool names, the same input parameters, and response objects whose keys and key order match the Financial Datasets OpenAPI schemas, captured 2026-09-04. It is an independent, Monid-backed implementation; it is not affiliated with or endorsed by Financial Datasets, and no Financial Datasets data or outputs are used.

## Authentication: bring your own Monid API key

Every request (REST and MCP) carries the caller's own Monid API key in the
`X-API-KEY` header. This is not a shared server key: it is passed straight
through to Monid, so **usage bills the caller's own Monid wallet**, not the
operator's. The server never logs or stores a caller's key. Get a key at
[monid.ai](https://monid.ai).

- Missing, empty, or malformed `X-API-KEY` answers `401 {"error": "unauthorized", ...}`.
- An operator may optionally set `API_KEYS` to further restrict which
  caller-supplied keys may reach the server at all; every accepted key is
  still the caller's own Monid key, never a shared backend credential.
- An operator may optionally enable a keyless demo mode (`DEMO_MONID_API_KEY`):
  keyless `GET` requests for the instant-tryout tickers (`AAPL`, `MSFT`,
  `NVDA`) run on the operator's own funded key, under a separate, stricter
  rate limit. Without that env var, keyless requests always get 401.

## Response contract

- Success responses contain only Financial Datasets schema keys, in schema property order. Unsourced optional fields are omitted rather than nulled or fabricated.
- All failures return the Financial Datasets `ErrorResponse` shape `{"error": <code>, "message": <detail>}`.
- Error codes in use: `bad_request`, `not_found`, `invalid_cursor`, `upstream_error`, `timeout`, `schema_drift`, `not_implemented`.
- Pagination matches the Financial Datasets style: `limit` is the total record budget across pages, `cursor` is an opaque base64url token, and `next_page_url` is present only when more records remain. Pages hold 10 records (100 for prices). `next_page_url` is built from the inbound request's own scheme and host, so it always points back at the deployment that served it; MCP clients should pass `cursor` back instead.
- Provenance, measured cost, and warnings never appear inside responses. They are appended to the ledger at `RECEIPTS_PATH` (conventionally `receipts/ledger.jsonl`) per Monid call, success or failure, by `fd.ReceiptsLedger` (`go/fd/receipts.go`); `ReceiptsLedger.SpentUSD()` totals it. Recording is best-effort observability: an unset or unwritable path disables it and never blocks a response.

## Tool status

All 27 Financial Datasets MCP tools listed in `go/mcpserver/tool_schemas.json`
are implemented against live Monid routes and contract-tested (see
`go/service/tools.go`'s `toolHandlers` table). The four ownership-state
tools (`get_beneficial_owners`, `get_beneficial_ownership`,
`get_insider_ownership`, `get_institutional_investors`) were the last to
land; see "Ownership-state data freshness" below for what they actually
source and how current that data is.

## REST route status

All 54 of Financial Datasets' REST paths are registered. 52 return data.
Two answer a zero-cost `not_implemented` stub (`/kpi/metrics/sectors`,
`/index-funds/tickers`), for the reasons noted beside each in
`notImplementedPaths` (`go/httpapi/rest.go`).

The four `as-reported` statement routes were the last to land. They read
the rendered statement files EDGAR generates from a filing's own XBRL
presentation linkbase, so the `line_items` tree is the filing's
hierarchy rather than a normalized feed reshaped to look like one. They
reproduce Financial Datasets' structure, not its labels: Apple's filing
prints "Gross margin" where Financial Datasets prints "Gross Profit",
and this server prints what the filing prints. See
`go/service/asreported.go` for what the rendered files cannot express.

Deliberate deviations, each forced by its source and each stated on the
route itself:

- `/ipos` and `/institutional-holdings/investors` require a `ticker`.
  Their feeds are keyed on one issuer and cannot enumerate across the
  market; Financial Datasets answers both unscoped.
- `/company/facts/ciks` returns the 8,005-CIK SEC ticker-file universe
  against Financial Datasets' 21,005, which also covers filers with no
  listed ticker. Every CIK returned is present in theirs.
- `/macro/interest-rates` reads each bank's current rate from its own page (Fed target-range midpoint, ECB deposit facility rate, BOE Bank Rate, BOJ policy rate) with `date` as the decision date. `bank` filters the list; `start_date`/`end_date` answer `bad_request` since no history is sourced. `/macro/interest-rates/banks` lists only those four banks. A bank whose page cannot be read is omitted; if none can, the call answers `upstream_error`.
  scrapes, not Financial Datasets' ten.
- `/filings/items/requests/{request_id}` answers `not_found` for any id.
  Filing-item extraction runs inline, so no request id is ever issued.

Three envelope-key details are worth naming, because each is a place where
reusing one implementation across two routes would have been wrong.
Financial Datasets keys `/activist-ownership` as `activist_owners` and
`/beneficial-ownership` as `beneficial_owners` despite both carrying the
same record, so the activist route re-keys the shared tool's envelope.
And `/institutional-holdings/investors` names its two fields `cik`/`name`,
while the same values are `filer_cik`/`filer_name` on
`InstitutionalHolding`; this server follows whichever spelling belongs to
the endpoint being served. Finally, `/financials` and
`/financials/as-reported` both answer under the key `financials`, but the
first carries one object holding three statement lists and the second
carries a paginated list of periods. Read the key by route, not by name.

## Cash flow statement source

Measured 2026-09-04 against SEC XBRL across eight large caps (AAPL, MSFT, XOM, KO, TSLA, PFE, VZ, NVDA), the normalized statements feed's cash flow subtotals came back: operating 8/8 correct, investing 0/8, financing 4/8, net change in cash 0/8. Apple FY2025 investing read 27,910,000,000 against the 10-K's 15,195,000,000; Microsoft FY2026 read -23,552,000,000 against SEC's -139,500,000,000.

The cash flow statement is therefore sourced from marketbeat, which matched SEC line for line on every figure checked. That applies on every path that builds one: `get_cash_flow_statement`, `get_all_financials`, and `search_line_items` when a cash flow field is requested. Income statement and balance sheet stay on the normalized feed, which agrees with SEC where measured.

Two consequences a caller will notice. marketbeat does not report `share_based_compensation` or `ending_cash_balance`, so those two fields are omitted on annual, quarterly and ttm cash flow records rather than carried over from a feed proven wrong on three of four subtotals. And the correction costs one extra provider call per statement, $0.02 measured, which is why `search_line_items` only pays it when the requested line items include a cash flow field.

The defect was reported to the upstream provider on 2026-09-04.

## Honest deviations from Financial Datasets behavior

- `get_beneficial_owners`/`get_beneficial_ownership` dedupe filers by name from the 13D/13G feed; `filer_cik` is matched only against whichever CIK-like alias a row happens to carry, which the verified item shape does not include, so a `filer_cik` query legitimately returns an empty result rather than a guess. `history=true` is accepted for schema parity but rejected: the feed carries only each stake's latest reported state, never a full amendment chain.
- `get_insider_ownership` derives its ticker's SEC CIK from the same filings lookup `get_filings` uses (reusing that call's cache), then aggregates SECForm4's insider-trading history down to each insider's most recent post-transaction share count. `form_type` is accepted for schema parity but rejected: this feed carries no Form 3/5 classification to filter by.
- `get_institutional_investors` lists distinct filers from an unscoped `/get_institution_holders` call; it is a directory of whatever filers currently appear in that feed, not a guaranteed-complete or authoritative 13F filer registry.
- `cik` parameters are accepted (contract parity) but answered with `bad_request`; only ticker lookup is routed. Company facts currently sources ticker and name only.
- `filing_type` accepts any of the 715 SEC EDGAR form types swept from EDGAR's own quarterly indexes (`go/service/filingtypes_gen.go`), covering all 500 Financial Datasets publishes. An earlier five-entry enum rejected real forms like `S-1` and `DEF 14A`.
- `get_news` requires `ticker`; market-wide news is not routed.
- Statement `filing_date*` filters need the filings join; if that join fails with filters active, the call answers `upstream_error` instead of guessing.
- TTM statements derive locally from four consecutive quarters (flows summed, weighted-average shares averaged, balances point-in-time). TTM records omit `fiscal_period` and `currency`.
- `get_stock_prices` returns ascending time order and aggregates day bars into week/month/year locally; `time` is the bar end date in UTC.
- `get_filing_items` accepts `include_exhibits` for parity but answers `bad_request` when set: exhibits are not sourced.
- `get_filings` validates `filing_type` against the EDGAR form-type catalog served at `/filings/types` (715 types); an unknown type answers `bad_request` naming that route.
- `list_filing_item_types` reuses each item's SEC title as `description`.
- `get_earnings` with a ticker composes records from 10-K and 10-Q filing events only; 8-K earnings releases are not composed. Without a ticker it answers the Nasdaq earnings calendar feed (five records). Records omit blocks whose period is not in the statements matrix.
- `get_financial_metrics` omits valuation fields (enterprise_value, price_to_* ratios, EV multiples, free_cash_flow_yield, peg_ratio, return_on_invested_capital, currency, filing_datetime): historical per-period market values are not sourceable without fabricating data. TTM rows omit filing identity (a TTM window spans filings). Margins/ROE/ROA use ending balances; turnovers use average balances.
- `get_insider_trades` is capped at the validated 15-row SECForm4 feed (FD allows 5000); `form_type` filtering is rejected because the route does not report form types. `name` is the full insider relationship text; title, transaction_code, security_title, and shares_owned_before_transaction are omitted.
- `screen_stocks` executes only `exchange` and `market_cap` filters with the `eq` operator via the Nasdaq screener; any other field or operator answers `bad_request` before spending. `list_stock_screener_filters` lists only those executable filters, not the full Financial Datasets catalog.
- Records omit fields the validated routes cannot source (for example CIK, currency, EBITDA on income statements); omitted keys are schema-legal.
