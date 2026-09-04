from __future__ import annotations

import math
import re
from dataclasses import dataclass
from datetime import date, datetime

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import SchemaDriftError

_TRANSACTION = re.compile(
    r"^(?P<date>[0-9]{4}-[0-9]{2}-[0-9]{2}) "
    r"(?P<action>[A-Za-z][A-Za-z /-]*)$"
)
_OWNERSHIP = re.compile(r"^(?P<shares>[^ ]+) \((?P<nature>[^()]+)\)$")
_NUMBER = re.compile(r"^-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$")
_MONEY = re.compile(r"^\$(?P<number>-?(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?)$")

INSIDER_UNSUPPORTED_FIELDS = (
    "form_type",
    "report_period",
    "transaction_code",
    "is_board_director",
    "security_title",
    "shares_owned_before_transaction",
    "exact_insider_name",
    "exact_insider_title",
)


@dataclass(frozen=True, slots=True)
class InsiderData:
    records: list[JsonObject]
    as_of: str | None


def normalize_insider_trades(
    value: JsonValue,
    *,
    ticker: str,
    limit: int,
    name: str | None,
    transaction_type: str | None,
    filing_date: date | None,
    filing_date_gte: date | None,
    filing_date_lte: date | None,
) -> InsiderData:
    root = _object(value, "SECForm4 search payload")
    if root.get("status") != "success":
        raise SchemaDriftError("SECForm4 search status must be 'success'")
    data = _object(root.get("data"), "SECForm4 search data")
    query = _string(data.get("query"), "SECForm4 search query")
    if query.upper() != ticker:
        raise SchemaDriftError("SECForm4 search query does not match the requested ticker")
    results = data.get("results")
    if not isinstance(results, list):
        raise SchemaDriftError("SECForm4 search results must be an array")
    if len(results) > 15:
        raise SchemaDriftError("SECForm4 search returned more than its validated 15-row ceiling")

    normalized: list[tuple[datetime, JsonObject]] = []
    for index, item in enumerate(results):
        row = _object(item, f"SECForm4 result[{index}]")
        symbol = _string(row.get("symbol"), f"SECForm4 result[{index}].symbol")
        if symbol.upper() != ticker:
            raise SchemaDriftError(f"SECForm4 result[{index}] symbol does not match {ticker}")
        transaction_day, action = _transaction(
            row.get("transaction_date"), f"SECForm4 result[{index}].transaction_date"
        )
        reported = _reported_datetime(
            row.get("reported_datetime"), f"SECForm4 result[{index}].reported_datetime"
        )
        relationship = _string(
            row.get("insider_relationship"),
            f"SECForm4 result[{index}].insider_relationship",
        )
        owned, ownership_nature = _owned_shares(
            row.get("shares_owned"), f"SECForm4 result[{index}].shares_owned"
        )
        record: JsonObject = {
            "ticker": ticker,
            "company": _string(row.get("company"), f"SECForm4 result[{index}].company"),
            "symbol": symbol,
            "transaction_date": transaction_day.isoformat(),
            "reported_datetime": reported.isoformat(),
            "filing_date": reported.date().isoformat(),
            "insider_relationship": relationship,
            "transaction_type": action,
            "shares_traded": _formatted_number(
                row.get("shares_traded"), f"SECForm4 result[{index}].shares_traded"
            ),
            "average_price": _formatted_money(
                row.get("average_price"), f"SECForm4 result[{index}].average_price"
            ),
            "total_amount": _formatted_money(
                row.get("total_amount"), f"SECForm4 result[{index}].total_amount"
            ),
            "shares_owned": owned,
            "ownership_nature": ownership_nature,
            "filing": _string(row.get("filing"), f"SECForm4 result[{index}].filing"),
            "filing_url": _string(
                row.get("filing_url"), f"SECForm4 result[{index}].filing_url"
            ),
            "symbol_url": _string(
                row.get("symbol_url"), f"SECForm4 result[{index}].symbol_url"
            ),
            "insider_relationship_url": _string(
                row.get("insider_relationship_url"),
                f"SECForm4 result[{index}].insider_relationship_url",
            ),
        }
        if name is not None and name.casefold() not in relationship.casefold():
            continue
        if transaction_type is not None and action.casefold() != transaction_type.casefold():
            continue
        reported_day = reported.date()
        if filing_date is not None and reported_day != filing_date:
            continue
        if filing_date_gte is not None and reported_day < filing_date_gte:
            continue
        if filing_date_lte is not None and reported_day > filing_date_lte:
            continue
        normalized.append((reported, record))

    normalized.sort(key=lambda item: item[0], reverse=True)
    selected = [item[1] for item in normalized[:limit]]
    as_of_value = selected[0].get("filing_date") if selected else None
    return InsiderData(selected, as_of_value if isinstance(as_of_value, str) else None)


def _transaction(value: JsonValue | None, name: str) -> tuple[date, str]:
    raw = _string(value, name)
    match = _TRANSACTION.fullmatch(raw)
    if match is None:
        raise SchemaDriftError(f"{name} must contain an ISO date and one action")
    try:
        day = date.fromisoformat(match.group("date"))
    except ValueError as error:
        raise SchemaDriftError(f"{name} contains an invalid date") from error
    return day, match.group("action")


def _reported_datetime(value: JsonValue | None, name: str) -> datetime:
    raw = _string(value, name)
    try:
        parsed = datetime.strptime(raw, "%Y-%m-%d %I:%M %p")
    except ValueError as error:
        raise SchemaDriftError(f"{name} must use YYYY-MM-DD h:mm am/pm") from error
    return parsed


def _owned_shares(value: JsonValue | None, name: str) -> tuple[int | float, str]:
    raw = _string(value, name)
    match = _OWNERSHIP.fullmatch(raw)
    if match is None:
        raise SchemaDriftError(f"{name} must contain shares followed by ownership text")
    return _parse_number(match.group("shares"), name), match.group("nature")


def _formatted_number(value: JsonValue | None, name: str) -> int | float:
    return _parse_number(_string(value, name), name)


def _formatted_money(value: JsonValue | None, name: str) -> int | float:
    raw = _string(value, name)
    match = _MONEY.fullmatch(raw)
    if match is None:
        raise SchemaDriftError(f"{name} must be an unambiguous dollar amount")
    return _parse_number(match.group("number"), name)


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
