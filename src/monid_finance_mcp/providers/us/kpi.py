"""Financial Datasets KPI extraction via SEC earnings-filing extraction.

Sector-specific operational KPIs, forward guidance, and non-GAAP metrics
are read from a single 10-K or 10-Q filing through Context.dev /web/extract
with a caller-supplied JSON Schema (factCheck true, maxDepth 0). Every
value is stated in the filing and carried through with its evidence quote;
fields the schema does not source are omitted.
"""
from __future__ import annotations

from dataclasses import dataclass
from math import isfinite

from monid_finance_mcp.fd import (
    kpi_guidance_item_record,
    kpi_metric_record,
    kpi_non_gaap_metric_record,
)
from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import InputError, SchemaDriftError

KPI_PERIODS = frozenset({"quarterly", "annual"})
KPI_FORMS = ("10-K", "10-Q")

_KPI_ITEM: JsonObject = {
    "type": "object",
    "additionalProperties": False,
    "properties": {
        "name": {"type": ["string", "null"]},
        "unit": {"type": ["string", "null"]},
        "period": {"type": ["string", "null"]},
        "value_text": {"type": ["string", "null"]},
        "value": {"type": ["number", "null"]},
        "basis": {"type": ["string", "null"]},
        "evidence_quote": {"type": ["string", "null"]},
    },
    "required": [
        "name",
        "unit",
        "period",
        "value_text",
        "value",
        "basis",
        "evidence_quote",
    ],
}


def _kpi_schema(description: str) -> JsonObject:
    return {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "additionalProperties": False,
        "properties": {"kpis": {"type": "array", "items": _KPI_ITEM}},
        "title": description,
    }


def kpi_metrics_extract_schema() -> JsonObject:
    return _kpi_schema("Operational key performance indicators")


def kpi_guidance_extract_schema() -> JsonObject:
    return _kpi_schema("Forward guidance")


def kpi_nongaap_extract_schema() -> JsonObject:
    return _kpi_schema("Non-GAAP financial metrics")


KPIS_BASE_INSTRUCTIONS = (
    "Extract from this earnings filing each disclosed {kind}. For every item "
    "return: name - a canonical snake_case metric name (e.g. load_factor, "
    "cet1_ratio, same_store_sales); unit - the unit of measure the filing uses "
    "(% , cents, USD, USD per share, count, etc.); period - the fiscal period "
    "label exactly as the filing states it (e.g. Q4 2025 or FY 2025); "
    "value_text - the number exactly as printed in the filing; value - the same "
    "number as a plain number; basis - exactly \"quarterly\" or \"annual\" "
    "according to the fiscal period being reported; evidence_quote - the "
    "sentence(s) from the filing that state the item. Use only numbers stated "
    "in the filing; leave fields null when the filing does not state them. "
    "Return an empty kpis list when none are disclosed."
)

KPI_METRICS_INSTRUCTIONS = KPIS_BASE_INSTRUCTIONS.format(
    kind="operational key performance indicators that do not appear on the "
    "standard financial statements, such as load factor, same-store sales, "
    "FFO per share, CET1 ratio, DAUs, or ARPU"
)

KPI_GUIDANCE_INSTRUCTIONS = KPIS_BASE_INSTRUCTIONS.format(
    kind="forward guidance, such as a guided revenue range, margin range, or "
    "EPS outlook; period is the forward fiscal period that is being guided"
)

KPI_NONGAAP_INSTRUCTIONS = KPIS_BASE_INSTRUCTIONS.format(
    kind="non-GAAP adjusted financial metrics, such as adjusted EPS, adjusted "
    "EBITDA, or free cash flow"
)


@dataclass(frozen=True, slots=True)
class KpiItem:
    name: str
    unit: str | None
    period: str | None
    value_text: str | None
    value: float | None
    basis: str | None
    evidence_quote: str | None


def validate_kpi_period(value: str, *, name: str = "period") -> str:
    normalized = value.strip().lower()
    if normalized not in KPI_PERIODS:
        raise InputError(f"{name} must be quarterly or annual")
    return normalized


def parse_kpi_items(data: JsonObject) -> list[KpiItem]:
    """Parse the extracted ``kpis`` list from a KPI extract envelope."""
    raw = data.get("kpis")
    if raw is None:
        return []
    if not isinstance(raw, list):
        raise SchemaDriftError("KPI extraction kpis must be an array or null")
    items: list[KpiItem] = []
    for index, item in enumerate(raw):
        if not isinstance(item, dict):
            raise SchemaDriftError(f"KPI extraction kpis[{index}] must be an object")
        name = item.get("name")
        unit = item.get("unit")
        period = item.get("period")
        value_text = item.get("value_text")
        raw_value = item.get("value")
        basis = item.get("basis")
        evidence = item.get("evidence_quote")
        if not isinstance(name, str) or not name:
            continue
        items.append(
            KpiItem(
                name=name,
                unit=unit if isinstance(unit, str) and unit else None,
                period=period if isinstance(period, str) and period else None,
                value_text=value_text if isinstance(value_text, str) and value_text else None,
                value=_finite_number(raw_value),
                basis=basis if isinstance(basis, str) and basis else None,
                evidence_quote=evidence if isinstance(evidence, str) and evidence else None,
            )
        )
    return items


def normalize_kpi_metrics(
    data: JsonObject,
    *,
    ticker: str,
    filing_url: str,
    period: str | None,
    metric_name: str | None,
) -> list[JsonObject]:
    records: list[JsonObject] = []
    for item in parse_kpi_items(data):
        if not _matches(item, period, metric_name):
            continue
        if not _complete(item):
            continue
        records.append(
            kpi_metric_record(
                ticker=ticker,
                metric_name=item.name,
                value=item.value,
                unit=item.unit if item.unit is not None else "",
                period=item.period if item.period is not None else "",
                period_type=item.basis or "",
                source_text=item.evidence_quote,
                source_url=filing_url,
            )
        )
    return records


def normalize_kpi_guidance(
    data: JsonObject,
    *,
    ticker: str,
    filing_url: str,
    period: str | None,
    metric_name: str | None,
) -> list[JsonObject]:
    records: list[JsonObject] = []
    for item in parse_kpi_items(data):
        if not _matches(item, period, metric_name):
            continue
        if not _complete(item):
            continue
        records.append(
            kpi_guidance_item_record(
                ticker=ticker,
                metric_name=item.name,
                value=item.value,
                unit=item.unit if item.unit is not None else "",
                period=item.period if item.period is not None else "",
                period_type=item.basis or "",
                raw_text=item.value_text,
                source_text=item.evidence_quote,
                source_url=filing_url,
            )
        )
    return records


def normalize_kpi_nongaap(
    data: JsonObject,
    *,
    ticker: str,
    filing_url: str,
    period: str | None,
    metric_name: str | None,
) -> list[JsonObject]:
    records: list[JsonObject] = []
    for item in parse_kpi_items(data):
        if not _matches(item, period, metric_name):
            continue
        if not _complete(item):
            continue
        records.append(
            kpi_non_gaap_metric_record(
                ticker=ticker,
                metric_name=item.name,
                value=item.value,
                unit=item.unit if item.unit is not None else "",
                period=item.period if item.period is not None else "",
                period_type=item.basis or "",
                source_text=item.evidence_quote,
                source_url=filing_url,
            )
        )
    return records


def _matches(item: KpiItem, period: str | None, metric_name: str | None) -> bool:
    if period is not None and (item.basis or "").lower() != period:
        return False
    return metric_name is None or _metric_key(item.name) == _metric_key(metric_name)


def _complete(item: KpiItem) -> bool:
    """Required Financial Datasets fields must all be present to emit a record."""
    return bool(item.unit and item.period and item.basis)


def _metric_key(value: str) -> str:
    return "_".join(value.strip().lower().split())


def _finite_number(value: JsonValue | None) -> float | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    result = float(value)
    return result if isfinite(result) else None
