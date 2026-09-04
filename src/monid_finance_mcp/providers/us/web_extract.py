"""Context.dev /web/extract request and response handling.

The validated route: POST with a caller-supplied JSON Schema,
factCheck true, maxDepth 0 (only the starting page). The output
envelope is ``{status, url, urls_analyzed, data}`` where ``data``
matches the supplied schema and unsupported fields come back null.
"""
from __future__ import annotations

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import SchemaDriftError

EXTRACT_ENDPOINT = "/web/extract"


def extract_request(
    *,
    url: str,
    schema: JsonObject,
    instructions: str,
    max_pages: int = 1,
) -> JsonObject:
    """Build the /web/extract request body for one page, fact-checked."""
    return {
        "url": url,
        "schema": schema,
        "instructions": instructions,
        "factCheck": True,
        "maxPages": max_pages,
        "maxDepth": 0,
    }


def parse_extract_output(value: JsonValue, *, expected_url: str) -> JsonObject:
    """Return the extracted ``data`` object, validating the envelope."""
    if not isinstance(value, dict):
        raise SchemaDriftError("Context.dev extract payload must be an object")
    payload = value
    if isinstance(payload.get("data"), dict) and "status" in payload:
        if payload.get("status") != "ok":
            raise SchemaDriftError("Context.dev extract did not report status ok")
        data = payload.get("data")
        if not isinstance(data, dict):
            raise SchemaDriftError("Context.dev extract data must be an object")
        return data
    del expected_url, payload
    raise SchemaDriftError("Context.dev extract envelope is missing status or data")
