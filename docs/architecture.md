# Phase 1 architecture

## Scope

Phase 1 serves US equities and ETFs. It exposes the Financial Datasets API contract: same 27 MCP tool names, same inputs, and response objects whose keys match the Financial Datasets OpenAPI schemas (captured 2026-09-04). Data is sourced independently through Monid; no Financial Datasets data or outputs are used. It does not claim the incumbent's latency, coverage, or redistribution rights.

## Flow

1. Validate the MCP input before spending money.
2. Inspect the selected Monid endpoint immediately before each run.
3. Execute one or more live Monid runs.
4. Poll bounded asynchronous runs.
5. Check lifecycle status and provider HTTP status separately.
6. Normalize provider output into a stable tool response.
7. For filing sections, validate the selected SEC URL before the scrape call.
8. Parse canonical SEC item headings locally and prefer body spans over table-of-contents spans.
9. Return only Financial Datasets schema keys; append a receipts ledger row for every Monid call (success or failure).

## Boundaries

- `client.py` owns Monid CLI execution and run-state handling.
- `compat.py` owns the Financial Datasets response primitives: ErrorResponse, opaque cursors, pagination, facade URLs.
- `fd.py` builds Financial Datasets record shapes in schema key order, omitting unsourced fields.
- `receipts.py` owns the append-only measured-receipts ledger (`receipts/ledger.jsonl`).
- `providers/us/` owns upstream adapters, the statements matrix parser, static SEC item maps, and local section parsing.
- `service.py` composes adapters into workflows and returns Financial Datasets responses or ErrorResponse objects.
- `server.py` registers all 27 Financial Datasets tools; unimplemented tools answer `not_implemented` at zero cost.

The provider boundary accepts a country now, but phase 1 rejects values other than `US`. India remains a separate adapter in phase 2.
