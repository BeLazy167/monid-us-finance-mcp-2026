from __future__ import annotations

import math
import re
from dataclasses import dataclass
from datetime import datetime

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import InputError, SchemaDriftError

_ALLOWED_EXCHANGES = ("NASDAQ", "NYSE", "AMEX")
_ALLOWED_MARKET_CAPS = ("mega", "large", "mid", "small", "micro", "nano")
_FILTER_FIELDS = frozenset({"field", "operator", "value"})
_NUMBER = re.compile(r"^-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$")
_MONEY = re.compile(r"^\$(?P<number>-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?)$")
_PERCENT = re.compile(r"^(?P<number>-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?)%$")
_AS_OF = re.compile(r"^Last price as of (?P<date>[A-Z][a-z]{2} [0-9]{1,2}, [0-9]{4})$")

SCREENER_CATALOG_SCOPE = (
    "Static local catalog for the two filters accepted by the validated Nasdaq route."
)


@dataclass(frozen=True, slots=True)
class ScreenerRequest:
    query_params: JsonObject
    limit: int
    offset: int


@dataclass(frozen=True, slots=True)
class ScreenerData:
    records: list[JsonObject]
    total_records: int
    upstream_as_of: str
    as_of: str


def validate_screener_request(
    filters: list[JsonObject], limit: int, offset: int
) -> ScreenerRequest:
    if not filters or len(filters) > 2:
        raise InputError("filters must contain one or two supported filters")
    if isinstance(limit, bool) or not 1 <= limit <= 100:
        raise InputError("limit must be between 1 and 100")
    if isinstance(offset, bool) or offset < 0:
        raise InputError("offset must be a non-negative integer")

    query: JsonObject = {"offset": offset, "limit": limit}
    seen: set[str] = set()
    for index, item in enumerate(filters):
        if set(item) != set(_FILTER_FIELDS):
            raise InputError(
                f"filters[{index}] must contain only field, operator, and value"
            )
        field = item.get("field")
        operator = item.get("operator")
        value = item.get("value")
        if not isinstance(field, str) or field not in {"exchange", "market_cap"}:
            raise InputError("only exchange and market_cap filters are supported")
        if field in seen:
            raise InputError(f"filter field {field} may appear only once")
        seen.add(field)
        if operator != "eq":
            raise InputError("only the eq stock-screener operator is supported")
        if not isinstance(value, str):
            raise InputError(f"{field} filter value must be a string")
        if field == "exchange":
            if value not in _ALLOWED_EXCHANGES:
                raise InputError("exchange must be NASDAQ, NYSE, or AMEX")
        elif value not in _ALLOWED_MARKET_CAPS:
            raise InputError("market_cap must be mega, large, mid, small, micro, or nano")
        query[field] = value
    return ScreenerRequest(query, limit, offset)


def normalize_screener(value: JsonValue) -> ScreenerData:
    root = _object(value, "Nasdaq stock-screener payload")
    if root.get("status") != "success":
        raise SchemaDriftError("Nasdaq stock-screener status must be 'success'")
    outer = _object(root.get("data"), "Nasdaq stock-screener data")
    data = _object(outer.get("data"), "Nasdaq stock-screener nested data")
    _object(data.get("filters"), "Nasdaq stock-screener filters")
    table = _object(data.get("table"), "Nasdaq stock-screener table")
    headers = _object(table.get("headers"), "Nasdaq stock-screener headers")
    expected_headers = {"symbol", "name", "lastsale", "netchange", "pctchange", "marketCap"}
    header_names_ok = set(headers) == expected_headers
    header_values_ok = all(isinstance(item, str) for item in headers.values())
    if not header_names_ok or not header_values_ok:
        raise SchemaDriftError("Nasdaq stock-screener headers changed")
    rows_value = table.get("rows")
    if not isinstance(rows_value, list):
        raise SchemaDriftError("Nasdaq stock-screener rows must be an array")
    total_records = data.get("totalrecords")
    if (
        isinstance(total_records, bool)
        or not isinstance(total_records, int)
        or total_records < 0
    ):
        raise SchemaDriftError("Nasdaq stock-screener totalrecords must be non-negative")
    upstream_as_of = _string(data.get("asof"), "Nasdaq stock-screener asof")
    as_of = _parse_as_of(upstream_as_of)
    provider_status = _object(outer.get("status"), "Nasdaq stock-screener response status")
    if provider_status.get("rCode") != 200:
        raise SchemaDriftError("Nasdaq stock-screener response status is not 200")

    records: list[JsonObject] = []
    for index, item in enumerate(rows_value):
        row = _object(item, f"Nasdaq stock-screener row[{index}]")
        records.append(
            {
                "ticker": _string(row.get("symbol"), f"Nasdaq row[{index}].symbol").upper(),
                "name": _string(row.get("name"), f"Nasdaq row[{index}].name"),
                "last_sale": _money(row.get("lastsale"), f"Nasdaq row[{index}].lastsale"),
                "net_change": _number(
                    row.get("netchange"), f"Nasdaq row[{index}].netchange"
                ),
                "percent_change": _percent(
                    row.get("pctchange"), f"Nasdaq row[{index}].pctchange"
                ),
                "market_cap": _number(
                    row.get("marketCap"), f"Nasdaq row[{index}].marketCap"
                ),
                "source_path": _string(row.get("url"), f"Nasdaq row[{index}].url"),
            }
        )
    return ScreenerData(records, total_records, upstream_as_of, as_of)


def screener_catalog() -> JsonObject:
    return {
        "filters": [
            {
                "field": "exchange",
                "operators": ["eq"],
                "values": list(_ALLOWED_EXCHANGES),
            },
            {
                "field": "market_cap",
                "operators": ["eq"],
                "values": list(_ALLOWED_MARKET_CAPS),
            },
        ],
        "source": "static local catalog",
        "catalog_scope": SCREENER_CATALOG_SCOPE,
    }


def _parse_as_of(value: str) -> str:
    match = _AS_OF.fullmatch(value)
    if match is None:
        raise SchemaDriftError("Nasdaq stock-screener asof changed format")
    try:
        return datetime.strptime(match.group("date"), "%b %d, %Y").date().isoformat()
    except ValueError as error:
        raise SchemaDriftError("Nasdaq stock-screener asof contains an invalid date") from error


def _money(value: JsonValue | None, name: str) -> int | float:
    raw = _string(value, name)
    match = _MONEY.fullmatch(raw)
    if match is None:
        raise SchemaDriftError(f"{name} must be an unambiguous dollar amount")
    return _parse_number(match.group("number"), name)


def _percent(value: JsonValue | None, name: str) -> float:
    raw = _string(value, name)
    match = _PERCENT.fullmatch(raw)
    if match is None:
        raise SchemaDriftError(f"{name} must be an unambiguous percentage")
    return float(_parse_number(match.group("number"), name)) / 100


def _number(value: JsonValue | None, name: str) -> int | float:
    return _parse_number(_string(value, name), name)


def _parse_number(raw: str, name: str) -> int | float:
    if _NUMBER.fullmatch(raw) is None:
        raise SchemaDriftError(f"{name} must be unambiguous numeric data")
    compact = raw.replace(",", "")
    value: int | float = float(compact) if "." in compact else int(compact)
    if not math.isfinite(value):
        raise SchemaDriftError(f"{name} must be finite numeric data")
    return value


def _object(value: JsonValue | None, name: str) -> JsonObject:
    if not isinstance(value, dict):
        raise SchemaDriftError(f"{name} must be an object")
    return value


def _string(value: JsonValue | None, name: str) -> str:
    if not isinstance(value, str) or not value:
        raise SchemaDriftError(f"{name} must be a non-empty string")
    return value
