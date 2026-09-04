from __future__ import annotations

import math
import re
from collections.abc import Iterable
from datetime import UTC, date, datetime
from typing import cast

from monid_finance_mcp.models import JsonObject, JsonValue

_TICKER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]{0,19}$")
_PERIODS = frozenset({"annual", "quarterly", "ttm"})
_INTERVALS = frozenset({"day", "week", "month", "year"})
_FILING_TYPES = frozenset({"10-K", "10-Q", "8-K", "20-F", "6-K"})
_STATEMENT_KEYS = {
    "income": ("incomeStatement", "income_statement", "income"),
    "balance": ("balanceSheet", "balance_sheet", "balance"),
    "cash": ("cashflow", "cashFlow", "cash_flow", "cashFlowStatement"),
}
_DATE_KEYS = (
    "report_period",
    "reportDate",
    "report_date",
    "periodEnding",
    "filingDate",
    "filing_date",
    "published_at",
    "publishedAt",
    "date",
    "end_date",
)


class InputError(ValueError):
    """A tool input is invalid and must fail before a paid call."""


class SchemaDriftError(ValueError):
    """A provider payload no longer matches a supported shape."""


def validate_ticker(value: str) -> str:
    ticker = value.strip().upper()
    if not _TICKER.fullmatch(ticker):
        raise InputError("ticker must be 1-20 letters, digits, dots, or hyphens")
    return ticker


def validate_period(value: str) -> str:
    period = value.strip().lower()
    if period not in _PERIODS:
        raise InputError("period must be annual, quarterly, or ttm")
    return period


def validate_interval(value: str) -> str:
    interval = value.strip().lower()
    if interval not in _INTERVALS:
        raise InputError("interval must be day, week, month, or year")
    return interval


def validate_limit(value: int, *, maximum: int) -> int:
    if isinstance(value, bool) or not 1 <= value <= maximum:
        raise InputError(f"limit must be between 1 and {maximum}")
    return value


def validate_date(value: str | None, name: str) -> date | None:
    if value is None:
        return None
    try:
        parsed = date.fromisoformat(value)
    except ValueError as error:
        raise InputError(f"{name} must use YYYY-MM-DD") from error
    if parsed.isoformat() != value:
        raise InputError(f"{name} must use YYYY-MM-DD")
    return parsed


def validate_date_range(
    start: str | None, end: str | None, start_name: str, end_name: str
) -> tuple[date | None, date | None]:
    start_date = validate_date(start, start_name)
    end_date = validate_date(end, end_name)
    if start_date is not None and end_date is not None and start_date > end_date:
        raise InputError(f"{start_name} must not be after {end_name}")
    return start_date, end_date


def validate_filing_types(values: list[str] | None) -> frozenset[str] | None:
    if values is None:
        return None
    normalized = frozenset(value.strip().upper() for value in values)
    if not normalized or not normalized <= _FILING_TYPES:
        allowed = ", ".join(sorted(_FILING_TYPES))
        raise InputError(f"filing_type values must be one of: {allowed}")
    return normalized


def normalize_summary(value: JsonValue, ticker: str) -> JsonObject:
    payload = _unwrap_mapping(value)
    numeric_keys = (
        "price",
        "currentPrice",
        "volume",
        "marketCap",
        "trailingPE",
        "revenueTTM",
    )
    for key in numeric_keys:
        field_value = payload.get(key)
        if field_value is not None and not _is_finite_number(field_value):
            raise SchemaDriftError(f"DefiLlama summary field {key} is not finite numeric data")
    has_numeric_data = any(_is_finite_number(payload.get(key)) for key in numeric_keys)
    if not payload or not has_numeric_data:
        raise SchemaDriftError("DefiLlama summary omitted recognized numeric market fields")
    return {
        "ticker": ticker,
        "as_of": payload_as_of(payload),
        "currency": _first_value(payload, ("currency", "currencyCode")),
        "price": _first_value(payload, ("price", "currentPrice")),
        "volume": _first_value(payload, ("volume",)),
        "market_cap": _first_value(payload, ("market_cap", "marketCap")),
        "enterprise_value": _first_value(payload, ("enterprise_value", "enterpriseValue")),
        "fifty_two_week_high": _first_value(payload, ("fiftyTwoWeekHigh",)),
        "fifty_two_week_low": _first_value(payload, ("fiftyTwoWeekLow",)),
        "dividend_yield": _first_value(payload, ("dividend_yield", "dividendYield")),
        "day_change": _first_value(payload, ("priceChange1d",)),
        "day_change_percent": _first_value(payload, ("priceChangePercentage1d",)),
        "price_to_earnings_ratio": _first_value(payload, ("trailingPE", "peRatio")),
        "price_to_revenue": _first_value(payload, ("priceToRevenue",)),
        "price_to_book": _first_value(payload, ("priceToBook",)),
        "enterprise_value_to_ebitda": _first_value(payload, ("enterpriseValueToEbitda",)),
        "revenue_ttm": _first_value(payload, ("revenueTTM",)),
        "gross_profit_ttm": _first_value(payload, ("grossProfitTTM",)),
        "net_income_ttm": _first_value(payload, ("earningsTTM",)),
        "ebitda_ttm": _first_value(payload, ("ebitdaTTM",)),
        "operating_profit_margin_ttm": _first_value(payload, ("operatingProfitMarginTTM",)),
        "provider_fields": payload,
    }


def normalize_stock_price(value: JsonValue, ticker: str) -> JsonObject:
    payload = normalize_summary(value, ticker)
    price = payload["price"]
    if not _is_finite_number(price):
        raise SchemaDriftError("DefiLlama summary omitted a finite numeric current price")
    return payload


def find_company(value: JsonValue, ticker: str) -> JsonObject | None:
    records = _extract_records(value, ("companies", "results", "data"))
    for record in records:
        symbol = _first_string(record, ("ticker", "symbol"))
        country = _first_string(record, ("country", "countryCode", "country_code"))
        if (
            symbol is not None
            and symbol.upper() == ticker
            and (country is None or country.upper() == "US")
        ):
            return {
                "ticker": ticker,
                "name": _first_string(record, ("name", "companyName", "company_name")),
                "country": country or "US",
                "country_name": _first_string(record, ("country_name", "countryName")),
                "exchange": _first_string(record, ("exchange", "exchangeName")),
                "sector": _first_string(record, ("sector",)),
                "industry": _first_string(record, ("industry",)),
                "employee_count": _first_value(record, ("employee_count", "employeeCount")),
                "provider_fields": record,
            }
    return None


def normalize_statement(
    value: JsonValue,
    *,
    statement: str,
    period: str,
    limit: int,
    report_period: date | None,
    report_period_gte: date | None,
    report_period_lte: date | None,
) -> list[JsonObject]:
    root = _unwrap_mapping(value)
    section: JsonValue | None = None
    for key in _STATEMENT_KEYS[statement]:
        if key in root:
            section = root[key]
            break
    if not isinstance(section, dict):
        raise SchemaDriftError(f"DefiLlama payload omitted the {statement} statement")

    selected = section.get(period)
    if selected is None:
        return []
    if isinstance(selected, list):
        records = _statement_records_from_objects(_objects(selected, f"{statement}.{period}"))
    elif isinstance(selected, dict):
        records = _matrix_records(section, selected, f"{statement}.{period}")
    else:
        raise SchemaDriftError(f"DefiLlama {statement}.{period} has an unknown shape")

    filtered = _filter_records_by_date(
        records,
        exact=report_period,
        minimum=report_period_gte,
        maximum=report_period_lte,
    )
    return sorted(filtered, key=_record_sort_key, reverse=True)[:limit]


def normalize_filings(
    value: JsonValue,
    *,
    filing_types: frozenset[str] | None,
    limit: int,
    filing_date_gte: date | None,
    filing_date_lte: date | None,
) -> list[JsonObject]:
    records = _extract_records(value, ("filings", "results", "data"))
    if filing_types is not None:
        records = [
            record
            for record in records
            if (_first_string(record, ("form", "filing_type", "type")) or "").upper()
            in filing_types
        ]
    records = _filter_records_by_date(
        records,
        exact=None,
        minimum=filing_date_gte,
        maximum=filing_date_lte,
        keys=("filingDate", "filing_date", "date"),
    )
    selected = sorted(records, key=_record_sort_key, reverse=True)[:limit]
    return [
        {
            "filing_date": _first_value(record, ("filing_date", "filingDate", "date")),
            "report_date": _first_value(record, ("report_date", "reportDate")),
            "form": _first_value(record, ("form", "filing_type", "type")),
            "primary_document_url": _first_value(
                record, ("primary_document_url", "primaryDocumentUrl", "url")
            ),
            "document_description": _first_value(
                record, ("document_description", "documentDescription", "description")
            ),
            "provider_fields": record,
        }
        for record in selected
    ]


def normalize_prices(
    value: JsonValue,
    *,
    start_date: date,
    end_date: date,
    interval: str,
) -> list[JsonObject]:
    raw_rows = _extract_array_rows(value)
    daily: list[JsonObject] = []
    for index, row in enumerate(raw_rows):
        if len(row) != 6:
            raise SchemaDriftError(f"OHLCV row {index} must contain six values")
        timestamp, raw_open, raw_high, raw_low, raw_close, raw_volume = row
        if isinstance(timestamp, bool) or not isinstance(timestamp, int | float):
            raise SchemaDriftError(f"OHLCV row {index} timestamp must be numeric")
        open_price = _number(raw_open, "open")
        high = _number(raw_high, "high")
        low = _number(raw_low, "low")
        close = _number(raw_close, "close")
        volume = _number(raw_volume, "volume")
        if high < low:
            raise SchemaDriftError(f"OHLCV row {index} high is below low")
        if volume < 0:
            raise SchemaDriftError(f"OHLCV row {index} volume is negative")
        instant = datetime.fromtimestamp(timestamp, tz=UTC)
        day = instant.date()
        if not start_date <= day <= end_date:
            continue
        daily.append(
            {
                "date": day.isoformat(),
                "timestamp": instant.isoformat().replace("+00:00", "Z"),
                "open": open_price,
                "high": high,
                "low": low,
                "close": close,
                "volume": volume,
            }
        )
    daily.sort(key=lambda row: cast(str, row["timestamp"]))
    if interval == "day":
        return daily
    return _aggregate_prices(daily, interval)


def normalize_news(
    value: JsonValue,
    *,
    limit: int,
    start_date: date | None,
    end_date: date | None,
) -> list[JsonObject]:
    records = _extract_records(value, ("news", "articles", "results", "data"))
    if start_date is not None or end_date is not None:
        records = _filter_records_by_date(
            records,
            exact=None,
            minimum=start_date,
            maximum=end_date,
            keys=("published_at", "publishedAt", "date"),
        )
    selected = sorted(records, key=_record_sort_key, reverse=True)[:limit]
    return [
        {
            "id": _first_value(record, ("id",)),
            "story_id": _first_value(record, ("story_id", "storyId")),
            "url": _first_value(record, ("url",)),
            "title": _first_value(record, ("title", "headline")),
            "description": _first_value(record, ("description", "summary")),
            "language": _first_value(record, ("language",)),
            "authors": _first_value(record, ("authors",)),
            "image_url": _first_value(record, ("image_url", "imageUrl")),
            "published_at": _first_value(record, ("published_at", "publishedAt", "date")),
            "article_type": _first_value(record, ("article_type", "type")),
            "source": _first_value(record, ("source",)),
            "match": _first_value(record, ("match",)),
            "provider_fields": record,
        }
        for record in selected
    ]


def latest_date(records: Iterable[JsonObject], keys: tuple[str, ...] = _DATE_KEYS) -> str | None:
    dates = [_record_date(record, keys) for record in records]
    known = [value for value in dates if value is not None]
    return max(known).isoformat() if known else None


def payload_as_of(value: JsonObject) -> str | None:
    for key in ("timestamp", "updatedAt", "asOf", "as_of", "date"):
        item = value.get(key)
        if isinstance(item, str) and item:
            return item
    return None


def _unwrap_mapping(value: JsonValue) -> JsonObject:
    current = value
    for _ in range(4):
        if not isinstance(current, dict):
            break
        if any(key in current for aliases in _STATEMENT_KEYS.values() for key in aliases):
            return current
        nested = current.get("data")
        if isinstance(nested, dict):
            current = nested
            continue
        return current
    if not isinstance(current, dict):
        raise SchemaDriftError("provider payload is not an object")
    return current


def _extract_records(value: JsonValue, keys: tuple[str, ...]) -> list[JsonObject]:
    current = value
    for _ in range(5):
        if isinstance(current, list):
            return _objects(current, "records")
        if not isinstance(current, dict):
            break
        found = False
        for key in keys:
            child = current.get(key)
            if isinstance(child, list):
                return _objects(child, key)
            if isinstance(child, dict):
                current = child
                found = True
                break
        if not found:
            break
    raise SchemaDriftError("provider payload omitted the expected record list")


def _objects(values: list[JsonValue], name: str) -> list[JsonObject]:
    records: list[JsonObject] = []
    for index, value in enumerate(values):
        if not isinstance(value, dict):
            raise SchemaDriftError(f"{name}[{index}] is not an object")
        records.append(value)
    return records


def _matrix_records(section: JsonObject, block: JsonObject, name: str) -> list[JsonObject]:
    labels_value = section.get("labels")
    dates_value = block.get("periodEnding")
    values_value = block.get("values")
    if (
        not isinstance(labels_value, list)
        or not isinstance(dates_value, list)
        or not isinstance(values_value, list)
    ):
        raise SchemaDriftError(f"{name} matrix omitted labels, periodEnding, or values")
    labels: list[str] = []
    for label in labels_value:
        if not isinstance(label, str):
            raise SchemaDriftError(f"{name} label must be a string")
        labels.append(label)
    dates: list[str] = []
    for period_end in dates_value:
        if not isinstance(period_end, str):
            raise SchemaDriftError(f"{name} periodEnding must contain strings")
        dates.append(period_end)
    if len(values_value) != len(labels):
        raise SchemaDriftError(f"{name} values row count does not match labels")
    rows: list[list[JsonValue]] = []
    for row_index, raw_row in enumerate(values_value):
        if not isinstance(raw_row, list) or len(raw_row) != len(dates):
            raise SchemaDriftError(f"{name} values row {row_index} has the wrong width")
        rows.append(raw_row)

    records: list[JsonObject] = []
    for column, period_end in enumerate(dates):
        line_items: list[JsonObject] = []
        provider_fields: JsonObject = {}
        for row_index, label in enumerate(labels):
            item_value = rows[row_index][column]
            line_items.append({"name": _field_name(label), "label": label, "value": item_value})
            provider_fields[label] = item_value
        records.append(
            {
                "report_period": period_end,
                "line_items": line_items,
                "provider_fields": provider_fields,
                "provider_structure": {"children": section.get("children")},
            }
        )
    return records


def _statement_records_from_objects(records: list[JsonObject]) -> list[JsonObject]:
    normalized: list[JsonObject] = []
    for record in records:
        report_period = _record_date(record)
        line_items: list[JsonObject] = []
        for key, value in record.items():
            if key in _DATE_KEYS or key in {"period", "frequency"}:
                continue
            line_items.append({"name": _field_name(key), "label": key, "value": value})
        normalized.append(
            {
                "report_period": report_period.isoformat() if report_period else None,
                "line_items": line_items,
                "provider_fields": record,
            }
        )
    return normalized


def _extract_array_rows(value: JsonValue) -> list[list[JsonValue]]:
    current = value
    for _ in range(5):
        if isinstance(current, list):
            rows: list[list[JsonValue]] = []
            for index, item in enumerate(current):
                if not isinstance(item, list):
                    raise SchemaDriftError(f"OHLCV row {index} is not an array")
                rows.append(item)
            return rows
        if not isinstance(current, dict):
            break
        next_value: JsonValue | None = None
        for key in ("ohlcv", "candles", "prices", "data"):
            child = current.get(key)
            if isinstance(child, list | dict):
                next_value = child
                break
        if next_value is None:
            break
        current = next_value
    raise SchemaDriftError("DefiLlama payload omitted OHLCV rows")


def _filter_records_by_date(
    records: list[JsonObject],
    *,
    exact: date | None,
    minimum: date | None,
    maximum: date | None,
    keys: tuple[str, ...] = _DATE_KEYS,
) -> list[JsonObject]:
    if exact is None and minimum is None and maximum is None:
        return records
    filtered: list[JsonObject] = []
    for record in records:
        record_date = _record_date(record, keys)
        if record_date is None:
            continue
        if exact is not None and record_date != exact:
            continue
        if minimum is not None and record_date < minimum:
            continue
        if maximum is not None and record_date > maximum:
            continue
        filtered.append(record)
    return filtered


def _record_date(record: JsonObject, keys: tuple[str, ...] = _DATE_KEYS) -> date | None:
    for key in keys:
        value = record.get(key)
        if not isinstance(value, str) or not value:
            continue
        try:
            return date.fromisoformat(value[:10])
        except ValueError:
            continue
    return None


def _record_sort_key(record: JsonObject) -> tuple[bool, date]:
    value = _record_date(record)
    return value is not None, value or date.min


def _first_value(record: JsonObject, keys: tuple[str, ...]) -> JsonValue:
    for key in keys:
        if key in record:
            return record[key]
    return None


def _field_name(label: str) -> str:
    words = re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", label)
    return re.sub(r"[^a-zA-Z0-9]+", "_", words).strip("_").lower()


def _first_string(record: JsonObject, keys: tuple[str, ...]) -> str | None:
    for key in keys:
        value = record.get(key)
        if isinstance(value, str) and value:
            return value
    return None


def _aggregate_prices(daily: list[JsonObject], interval: str) -> list[JsonObject]:
    groups: dict[str, list[JsonObject]] = {}
    for row in daily:
        day = date.fromisoformat(cast(str, row["date"]))
        if interval == "week":
            iso_year, iso_week, _ = day.isocalendar()
            key = f"{iso_year}-W{iso_week:02d}"
        elif interval == "month":
            key = f"{day.year:04d}-{day.month:02d}"
        else:
            key = f"{day.year:04d}"
        groups.setdefault(key, []).append(row)

    result: list[JsonObject] = []
    for key, rows in groups.items():
        highs = [_number(row["high"], "high") for row in rows]
        lows = [_number(row["low"], "low") for row in rows]
        volumes = [_number(row["volume"], "volume") for row in rows]
        result.append(
            {
                "period": key,
                "start_date": rows[0]["date"],
                "end_date": rows[-1]["date"],
                "open": rows[0]["open"],
                "high": max(highs),
                "low": min(lows),
                "close": rows[-1]["close"],
                "volume": sum(volumes),
            }
        )
    return result


def _is_finite_number(value: JsonValue) -> bool:
    return isinstance(value, int | float) and not isinstance(value, bool) and math.isfinite(value)


def _number(value: JsonValue, name: str) -> int | float:
    if isinstance(value, bool) or not isinstance(value, int | float) or not math.isfinite(value):
        raise SchemaDriftError(f"OHLCV {name} must be finite and numeric")
    return value
