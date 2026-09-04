"""Financial Datasets institutional holdings via SECForm4 institution holders.

Ticker mode reads the validated SECForm4 /get_institution_holders route
and maps the reported holder rows onto Financial Datasets
InstitutionalHolding fields that the payload sources (name_of_issuer,
shares, value_usd, report_period, filer_name). Filer-CIK mode is not
routed, so the service answers with an honest bad_request.
"""
from __future__ import annotations

from datetime import date

from monid_finance_mcp.fd import institutional_holding_record
from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import SchemaDriftError

INSTITUTIONAL_ENDPOINT = "/get_institution_holders"

_ROW_KEYS = ("results", "holders", "rows", "data", "table", "institutionHolders")


def normalize_institutional_holdings(
    value: JsonValue,
    *,
    ticker: str,
    limit: int,
    report_period: dict[str, date | None],
) -> list[JsonObject]:
    """Map SECForm4 holder rows to FD InstitutionalHolding records, filtered."""
    root = _object(value, "SECForm4 institution holders payload")
    if root.get("status") != "success":
        raise SchemaDriftError("SECForm4 institution holders status must be 'success'")
    data = root.get("data")
    if isinstance(data, list):
        rows = _object_list(data, "SECForm4 institution holders rows")
        issuer_name: str | None = None
    elif isinstance(data, dict):
        issuer_name = _first_string(
            data, ("name_of_issuer", "companyName", "company", "issuer")
        )
        rows = _holder_rows(data)
    else:
        raise SchemaDriftError("SECForm4 institution holders omitted a data object")

    records: list[JsonObject] = []
    for index, row in enumerate(rows):
        validated = _object(row, f"SECForm4 holder row[{index}]")
        report_day = _first_date(
            validated, ("report_period", "reportDate", "date", "as_of", "periodEnd")
        )
        if not _matches(report_day, report_period):
            continue
        filer_name = _first_string(
            validated,
            ("filer_name", "institution", "holder", "manager", "name"),
        )
        shares = _first_int(
            validated,
            ("shares", "shares_held", "num_shares", "position_shares", "current_shares"),
        )
        value_usd = _first_int(
            validated,
            ("value_usd", "market_value", "position_value", "current_value", "value"),
        )
        row_issuer = _first_string(
            validated, ("name_of_issuer", "company", "issuer")
        )
        record = institutional_holding_record(
            ticker=ticker,
            name_of_issuer=row_issuer if row_issuer is not None else issuer_name,
            shares=shares,
            value_usd=value_usd,
            report_period=report_day.isoformat() if report_day is not None else None,
            filer_name=filer_name,
        )
        if filer_name is None and shares is None and value_usd is None:
            continue
        records.append(record)
    records.sort(
        key=lambda record: (
            isinstance(record.get("value_usd"), int),
            record.get("value_usd") if isinstance(record.get("value_usd"), int) else 0,
        ),
        reverse=True,
    )
    return records[:limit]


def _holder_rows(data: JsonObject) -> list[JsonObject]:
    for key in _ROW_KEYS:
        child = data.get(key)
        if isinstance(child, list):
            return _object_list(child, f"SECForm4 holder {key}")
    raise SchemaDriftError("SECForm4 institution holders omitted the holder table")


def _matches(day: date | None, report_period: dict[str, date | None]) -> bool:
    exact = report_period.get("exact")
    gte = report_period.get("gte")
    lte = report_period.get("lte")
    gt = report_period.get("gt")
    lt = report_period.get("lt")
    if exact is None and gte is None and lte is None and gt is None and lt is None:
        return True
    if day is None:
        return False
    if exact is not None and day != exact:
        return False
    if gte is not None and day < gte:
        return False
    if lte is not None and day > lte:
        return False
    if gt is not None and day <= gt:
        return False
    return lt is None or day < lt


def _object(value: JsonValue | None, name: str) -> JsonObject:
    if not isinstance(value, dict):
        raise SchemaDriftError(f"{name} must be an object")
    return value


def _object_list(value: list[JsonValue], name: str) -> list[JsonObject]:
    rows: list[JsonObject] = []
    for index, item in enumerate(value):
        if not isinstance(item, dict):
            raise SchemaDriftError(f"{name}[{index}] must be an object")
        rows.append(item)
    return rows


def _first_string(row: JsonObject, keys: tuple[str, ...]) -> str | None:
    for key in keys:
        value = row.get(key)
        if isinstance(value, str) and value:
            return value
    return None


def _first_date(row: JsonObject, keys: tuple[str, ...]) -> date | None:
    for key in keys:
        value = row.get(key)
        if not isinstance(value, str) or not value:
            continue
        try:
            return date.fromisoformat(value[:10])
        except ValueError:
            continue
    return None


def _first_int(row: JsonObject, keys: tuple[str, ...]) -> int | None:
    for key in keys:
        value = row.get(key)
        if isinstance(value, bool):
            continue
        if isinstance(value, int):
            return value
        if isinstance(value, float) and value.is_integer():
            return int(value)
    return None
