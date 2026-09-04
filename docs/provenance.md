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
- DefiLlama `/equities/v1/filings` for the filing index and filing selection;
- DefiLlama `/equities/v1/ohlcv` for delayed or EOD price history;
- Context.dev `/news/search` for ticker-matched company news;
- Context.dev `/web/scrape/markdown` for selected SEC filing markdown.

Planned sources for later slices include:

- DefiLlama beta equities: dimensions and other supported equity data.
- Nasdaq: earnings, quotes, filings, ownership, and market calendars.
- SECForm4: Forms 3/4/5, Schedules 13D/13G, and 13F-derived views where available.
- Context.dev: relevance-scored news and filing/page extraction.
- Finviz or Nasdaq: stock screening.

Every response identifies the exact provider endpoint. Provider availability, source licenses, and redistribution rights remain provider-specific.

## Static SEC filing-item catalog

`list_filing_item_types` makes no paid call. It uses the revision identifier printed on each official SEC form:

- Form 10-K, `SEC 1673 (02-25)`: https://www.sec.gov/files/form10-k.pdf
- Form 10-Q, `SEC 1296 (02-25)`: https://www.sec.gov/files/form10-q.pdf
- Form 8-K, `SEC 873 (02-25)`: https://www.sec.gov/files/form8-k.pdf

The response exposes these revisions as `SEC-1673-02-25`, `SEC-1296-02-25`, and `SEC-873-02-25` catalog versions.

The catalog distinguishes Part I and Part II for Form 10-Q. It includes the current cybersecurity items, Item 1C in Form 10-K and Item 1.05 in Form 8-K. The static catalog does not show or imply Monid upstream support.

## Filing section extraction

`get_filing_items` uses the SEC `reportDate` year and optional quarter. An optional accession number must match the accession derived from the filing URL. The service rejects any selected document URL outside `https://sec.gov/Archives/` or `https://www.sec.gov/Archives/` before it requests a scrape.

Context.dev receives `includeLinks=false`, `includeImages=false`, `useMainContentOnly=true`, and `timeoutMS=30000`. A deterministic local parser ignores lines that retain inline-XBRL tags. It matches canonical item headings and excludes table-of-contents candidates. It ranks duplicate body headings by span length, title match, and document position. It does not call an LLM.
