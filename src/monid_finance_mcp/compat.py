from __future__ import annotations

import base64
import binascii
import json
from dataclasses import dataclass
from typing import cast
from urllib.parse import urlencode

from monid_finance_mcp.models import JsonObject, JsonValue

COMPAT_BASE_URL = "https://api.monid-finance-mcp.example"
DEFAULT_PAGE_SIZE = 10
PRICES_PAGE_SIZE = 100


class CursorError(ValueError):
    """A caller-supplied cursor is not a valid opaque cursor from this server."""


def fd_error(error: str, message: str) -> JsonObject:
    """Financial Datasets ErrorResponse shape."""
    return {"error": error, "message": message}


def encode_cursor(offset: int, *, filters: JsonObject) -> str:
    payload = {"o": offset, "f": dict(sorted(filters.items()))}
    raw = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return base64.urlsafe_b64encode(raw).decode("ascii").rstrip("=")


def decode_cursor(cursor: str) -> tuple[int, JsonObject]:
    try:
        padded = cursor + "=" * (-len(cursor) % 4)
        raw = base64.urlsafe_b64decode(padded.encode("ascii"))
        loaded: object = json.loads(raw)
    except (binascii.Error, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise CursorError("cursor is not a valid opaque pagination token") from error
    if not isinstance(loaded, dict):
        raise CursorError("cursor payload must be an object")
    payload = cast(dict[object, object], loaded)
    offset_value = payload.get("o")
    filters_value = payload.get("f")
    if isinstance(offset_value, bool) or not isinstance(offset_value, int) or offset_value < 0:
        raise CursorError("cursor offset must be a non-negative integer")
    if not isinstance(filters_value, dict):
        raise CursorError("cursor filters must be an object with string keys")
    raw_filters = cast(dict[object, object], filters_value)
    filters: JsonObject = {}
    for key, value in raw_filters.items():
        if not isinstance(key, str):
            raise CursorError("cursor filters must have string keys")
        filters[key] = cast(JsonValue, value)
    return offset_value, filters


def next_page_url(path: str, *, cursor: str) -> str:
    return f"{COMPAT_BASE_URL}{path}?{urlencode({'cursor': cursor})}"


@dataclass(frozen=True, slots=True)
class Page:
    """One Financial Datasets style page of a locally filtered result set."""

    records: list[JsonObject]
    next_cursor: str | None
    next_url: str | None


def paginate(
    records: list[JsonObject],
    *,
    offset: int,
    path: str,
    page_size: int = DEFAULT_PAGE_SIZE,
    filters_for_cursor: JsonObject | None = None,
) -> Page:
    """Slice a filtered record list into one page plus an opaque continuation."""
    if page_size < 1:
        raise ValueError("page_size must be positive")
    page = records[offset : offset + page_size]
    next_offset = offset + page_size
    if next_offset < len(records) and page:
        filters = filters_for_cursor if filters_for_cursor is not None else {}
        cursor = encode_cursor(next_offset, filters=filters)
        return Page(page, cursor, next_page_url(path, cursor=cursor))
    return Page(page, None, None)
