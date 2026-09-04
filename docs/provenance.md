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

- DefiLlama beta equities: company catalog, market summary, statements, dimensions, filings, and OHLCV.
- Nasdaq: earnings, quotes, filings, ownership, and market calendars.
- SECForm4: Forms 3/4/5, Schedules 13D/13G, and 13F-derived views where available.
- Context.dev: relevance-scored news and filing/page extraction.
- Finviz or Nasdaq: stock screening.

Every response identifies the exact provider endpoint. Provider availability, source licenses, and redistribution rights remain provider-specific.
