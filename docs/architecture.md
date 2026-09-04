# Phase 1 architecture

## Scope

Phase 1 serves US equities and ETFs. It matches published MCP tool names and workflow intent. It does not claim the incumbent's latency, coverage, redistribution rights, or exact proprietary normalization.

## Flow

1. Validate the MCP input before spending money.
2. Inspect the selected Monid endpoint immediately before each run.
3. Execute one or more live Monid runs.
4. Poll bounded asynchronous runs.
5. Check lifecycle status and provider HTTP status separately.
6. Normalize provider output into a stable tool response.
7. Return provenance, run IDs, measured cost, warnings, and partial errors.

## Boundaries

- `client.py` owns Monid CLI execution and run-state handling.
- `models.py` owns stable envelopes and validation.
- `providers/us/` owns upstream adapters and normalization.
- `service.py` composes adapters into financial workflows.
- `server.py` exposes the compatibility tools through FastMCP.

The provider boundary accepts a country now, but phase 1 rejects values other than `US`. India remains a separate adapter in phase 2.
