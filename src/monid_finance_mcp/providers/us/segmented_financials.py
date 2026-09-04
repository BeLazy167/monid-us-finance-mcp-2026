"""Financial Datasets segmented financials via SEC filing extraction.

Uses the live-validated Apple 10-K extraction schema: product net
sales and geographic reportable segment net sales with per-period
values and evidence quotes, restricted to facts stated in the filing.
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import date

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import SchemaDriftError

_SEGMENT_ARRAY_ITEM: JsonObject = {
    "type": "object",
    "additionalProperties": False,
    "properties": {
        "name": {"type": ["string", "null"]},
        "metric": {"type": ["string", "null"]},
        "unit": {"type": ["string", "null"]},
        "values": {
            "type": "array",
            "items": {
                "type": "object",
                "additionalProperties": False,
                "properties": {
                    "fiscal_year": {"type": ["integer", "null"]},
                    "period_end": {"type": ["string", "null"]},
                    "value": {"type": ["number", "null"]},
                },
                "required": ["fiscal_year", "period_end", "value"],
            },
        },
        "evidence_quote": {"type": ["string", "null"]},
        "evidence_section": {"type": ["string", "null"]},
    },
    "required": ["name", "metric", "unit", "values", "evidence_quote", "evidence_section"],
}


def segment_extract_schema() -> JsonObject:
    """The JSON Schema sent to Context.dev for segment extraction."""
    return {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "additionalProperties": False,
        "properties": {
            "company": {"type": ["string", "null"]},
            "filing_type": {"type": ["string", "null"]},
            "fiscal_year_end": {"type": ["string", "null"]},
            "source_url": {"type": ["string", "null"]},
            "product_net_sales": {"type": "array", "items": _SEGMENT_ARRAY_ITEM},
            "geographic_reportable_segment_net_sales": {
                "type": "array",
                "items": _SEGMENT_ARRAY_ITEM,
            },
        },
    }


SEGMENT_INSTRUCTIONS = (
    "Extract the segment information note from this SEC filing: net sales "
    "by product or service line, and net sales by geographic reportable "
    "segment, for every fiscal year shown. Use only numbers stated in the "
    "filing; leave fields null when the filing does not state them."
)


@dataclass(frozen=True, slots=True)
class SegmentRow:
    label: str
    value: float


@dataclass(frozen=True, slots=True)
class SegmentPeriod:
    report_period: date
    fiscal_year: int | None
    products: list[SegmentRow]
    segments: list[SegmentRow]


def normalize_segmented_financials(
    data: JsonObject,
    *,
    ticker: str,
    filing_url: str,
    accession_number: str | None,
    form_type: str | None,
) -> list[JsonObject]:
    """Map extracted segment data to Financial Datasets records."""
    periods: dict[date, SegmentPeriod] = {}
    _collect_periods(data.get("product_net_sales"), periods, products=True)
    _collect_periods(data.get("geographic_reportable_segment_net_sales"), periods, products=False)
    if not periods:
        raise SchemaDriftError("Segment extraction returned no periods with values")
    records: list[JsonObject] = []
    for day in sorted(periods, reverse=True):
        period = periods[day]
        record: JsonObject = {
            "ticker": ticker,
            "report_period": day.isoformat(),
        }
        if period.fiscal_year is not None:
            record["fiscal_period"] = f"FY{period.fiscal_year}"
        record["period"] = "annual"
        if accession_number is not None:
            record["accession_number"] = accession_number
        if filing_url is not None:
            record["filing_url"] = filing_url
        if form_type is not None:
            del form_type
        income_statement: JsonObject = {}
        if period.products:
            income_statement["revenue"] = {
                "product": [
                    {"label": row.label, "value": row.value} for row in period.products
                ]
            }
        if period.segments:
            revenue = income_statement.setdefault("revenue", {})
            if isinstance(revenue, dict):
                revenue["segment"] = [
                    {"label": row.label, "value": row.value} for row in period.segments
                ]
        if income_statement:
            record["income_statement"] = income_statement
        records.append(record)
    return records


def _collect_periods(
    value: JsonValue, periods: dict[date, SegmentPeriod], *, products: bool
) -> None:
    if value is None:
        return
    if not isinstance(value, list):
        raise SchemaDriftError("Segment extraction arrays must be lists or null")
    for item in value:
        if not isinstance(item, dict):
            raise SchemaDriftError("Segment extraction rows must be objects")
        name = item.get("name")
        values = item.get("values")
        if not isinstance(name, str) or not isinstance(values, list):
            continue
        for entry in values:
            if not isinstance(entry, dict):
                continue
            period_end = entry.get("period_end")
            raw_value = entry.get("value")
            fiscal_year = entry.get("fiscal_year")
            if not isinstance(period_end, str) or raw_value is None:
                continue
            if isinstance(raw_value, bool) or not isinstance(raw_value, int | float):
                continue
            day = _opt_date(period_end)
            if day is None:
                continue
            row = SegmentRow(label=name, value=float(raw_value))
            existing = periods.get(day)
            year = fiscal_year if isinstance(fiscal_year, int) else None
            if existing is None:
                periods[day] = SegmentPeriod(
                    report_period=day,
                    fiscal_year=year,
                    products=[row] if products else [],
                    segments=[] if products else [row],
                )
            else:
                target = existing.products if products else existing.segments
                target.append(row)


def _opt_date(value: str) -> date | None:
    try:
        return date.fromisoformat(value[:10])
    except ValueError:
        return None
