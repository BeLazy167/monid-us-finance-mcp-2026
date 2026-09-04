# Data provenance

## Target contract

- https://docs.financialdatasets.ai/mcp-server
- https://www.financialdatasets.ai/openapi.json
- https://www.financialdatasets.ai/pricing

The public API contract is a reference for interoperability. This project does not use the Financial Datasets service or copy proprietary data.

## Routing platform

- https://monid.ai/SKILL.md
- https://docs.monid.ai/api/overview.html
- https://docs.monid.ai/api/run.html

The server calls Monid through its authenticated CLI. Each provider endpoint is inspected immediately before execution because schemas and prices can change.

## Initial live US sources

The first implemented slice uses these exact Monid routes:

- DefiLlama `/equities/v1/companies-list` and `/equities/v1/summary` for company facts;
- DefiLlama `/equities/v1/statements` for annual and quarterly statements;
- DefiLlama `/equities/v1/summary` for financial metrics and latest price snapshots;
- DefiLlama `/equities/v1/filings` for the filing index;
- DefiLlama `/equities/v1/ohlcv` for delayed or EOD price history;
- Context.dev `/news/search` for ticker-matched company news.

Planned sources for later slices include:

- DefiLlama beta equities: dimensions and other supported equity data.
- Nasdaq: earnings, quotes, filings, ownership, and market calendars.
- SECForm4: Forms 3/4/5, Schedules 13D/13G, and 13F-derived views where available.
- Context.dev: relevance-scored news and filing/page extraction.
- Finviz or Nasdaq: stock screening.

Every response identifies the exact provider endpoint. Provider availability, source licenses, and redistribution rights remain provider-specific.
