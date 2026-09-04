"""DefiLlama /statements parsing shared by the Financial Datasets tools.

Parses the matrix layout (labels x periodEnding values, with per-parent
children blocks) into per-period value rows keyed by plain statement
labels, derives fiscal-period labels, and derives trailing-twelve-month
windows from consecutive quarters.
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import date

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import SchemaDriftError

STATEMENT_SECTIONS = {
    "income": ("incomeStatement", "income_statement", "income"),
    "balance": ("balanceSheet", "balance_sheet", "balance"),
    "cash": ("cashflow", "cashFlow", "cash_flow", "cashFlowStatement"),
}


@dataclass(frozen=True, slots=True)
class PeriodRow:
    """One report period of one statement, with plain-label values."""

    report_period: date
    values: dict[str, JsonValue]


@dataclass(frozen=True, slots=True)
class StatementSeries:
    annual: list[PeriodRow]
    quarterly: list[PeriodRow]


def parse_statement_series(value: JsonValue, statement: str) -> StatementSeries:
    """Parse the annual and quarterly matrices of one statement."""
    if not isinstance(value, dict):
        raise SchemaDriftError("DefiLlama statements payload must be an object")
    root: JsonObject = value
    section = _section_root(root, statement)
    return StatementSeries(
        annual=_parse_matrix(section, statement, "annual"),
        quarterly=_parse_matrix(section, statement, "quarterly"),
    )


def fiscal_year_end_month(series: StatementSeries) -> int | None:
    """Learn the fiscal year end month from the annual series."""
    months = {row.report_period.month for row in series.annual}
    return months.pop() if len(months) == 1 else None


def fiscal_period_label(
    row: PeriodRow, year_end_month: int | None, *, is_annual: bool
) -> str | None:
    """Derive the Financial Datasets fiscal_period label for one row."""
    if is_annual:
        return f"FY{row.report_period.year}"
    if year_end_month is None:
        return None
    year = row.report_period.year
    if row.report_period.month > year_end_month:
        year += 1
    quarter = ((row.report_period.month - 1) // 3) + 1
    return f"Q{quarter} FY{year}"


def derive_ttm_rows(
    quarterly: list[PeriodRow], *, flow_labels: frozenset[str], mean_labels: frozenset[str]
) -> list[PeriodRow]:
    """Derive TTM rows from windows of four consecutive quarters.

    Flow labels are summed across the window; mean labels (weighted
    average share counts) are averaged; every other label keeps the
    value from the final quarter of the window (point-in-time balances).
    """
    ordered = sorted(quarterly, key=lambda row: row.report_period)
    rows: list[PeriodRow] = []
    for start in range(len(ordered) - 3):
        window = ordered[start : start + 4]
        if not _consecutive_quarters(window):
            continue
        last = window[3]
        values: dict[str, JsonValue] = {}
        labels: set[str] = set(last.values)
        for row in window:
            labels.update(row.values)
        for label in labels:
            window_values = [row.values.get(label) for row in window]
            numbers = [
                v for v in window_values if isinstance(v, int | float) and not isinstance(v, bool)
            ]
            if len(numbers) != 4:
                continue
            if label in flow_labels:
                values[label] = sum(numbers)
            elif label in mean_labels:
                values[label] = sum(numbers) / 4
            else:
                values[label] = numbers[3]
        rows.append(PeriodRow(report_period=last.report_period, values=values))
    return rows


def filter_rows(
    rows: list[PeriodRow],
    *,
    exact: date | None,
    gte: date | None,
    lte: date | None,
    gt: date | None,
    lt: date | None,
) -> list[PeriodRow]:
    """Apply Financial Datasets report_period comparison filters."""

    def keep(row: PeriodRow) -> bool:
        day = row.report_period
        if exact is not None and day != exact:
            return False
        if gte is not None and day < gte:
            return False
        if lte is not None and day > lte:
            return False
        if gt is not None and day <= gt:
            return False
        return lt is None or day < lt

    return [row for row in rows if keep(row)]


def _consecutive_quarters(window: list[PeriodRow]) -> bool:
    previous: int | None = None
    for row in window:
        ordinal = row.report_period.year * 4 + (row.report_period.month - 1) // 3
        if previous is not None and ordinal != previous + 1:
            return False
        previous = ordinal
    return True


def _section_root(root: JsonObject, statement: str) -> JsonObject:
    for key in STATEMENT_SECTIONS[statement]:
        section = root.get(key)
        if isinstance(section, dict):
            return section
    raise SchemaDriftError(f"DefiLlama payload omitted the {statement} statement")


def _parse_matrix(section: JsonObject, statement: str, period: str) -> list[PeriodRow]:
    block = section.get(period)
    if block is None:
        return []
    if not isinstance(block, dict):
        raise SchemaDriftError(f"DefiLlama {statement}.{period} has an unknown shape")
    labels = _labels(section.get("labels"), f"DefiLlama {statement} labels")
    dates = _dates(block.get("periodEnding"), f"DefiLlama {statement}.{period}.periodEnding")
    values = _value_rows(
        block.get("values"),
        row_count=len(labels),
        column_count=len(dates),
        name=f"DefiLlama {statement}.{period}.values",
    )
    rows = [PeriodRow(report_period=day, values={}) for day in dates]
    for row_index, label in enumerate(labels):
        for column in range(len(dates)):
            item = values[row_index][column]
            if item is not None:
                rows[column].values[label] = item
    children_block = block.get("children")
    child_definitions = section.get("children")
    if isinstance(children_block, dict) and isinstance(child_definitions, dict):
        _parse_children(
            children_block,
            child_definitions,
            period,
            dates,
            rows,
            name=f"DefiLlama {statement}.{period}",
        )
    return rows


def _parse_children(
    children_block: JsonObject,
    child_definitions: JsonObject,
    period: str,
    dates: list[date],
    rows: list[PeriodRow],
    *,
    name: str,
) -> None:
    definitions = child_definitions.get(period)
    if not isinstance(definitions, dict):
        return
    for parent, parent_block in children_block.items():
        if not isinstance(parent_block, dict):
            raise SchemaDriftError(f"{name} children must map parents to blocks")
        definition_value = definitions.get(parent)
        if definition_value is None:
            continue
        if not isinstance(definition_value, dict):
            raise SchemaDriftError(f"{name} child labels for {parent} must be a list")
        definition = definition_value
        child_labels_value = definition.get("labels")
        child_values_value = parent_block.get("values")
        if child_labels_value is None and child_values_value is None:
            continue
        if not isinstance(child_labels_value, list):
            raise SchemaDriftError(f"{name} child labels for {parent} must be a list")
        if not isinstance(child_values_value, list):
            raise SchemaDriftError(f"{name} child values for {parent} must be a list")
        child_labels = [label for label in child_labels_value if isinstance(label, str)]
        child_values = _value_rows(
            child_values_value,
            row_count=len(child_labels),
            column_count=len(dates),
            name=f"{name} children of {parent}",
        )
        for row_index, label in enumerate(child_labels):
            for column in range(len(dates)):
                item = child_values[row_index][column]
                if item is not None:
                    rows[column].values[f"{parent}|{label}"] = item


def _labels(value: JsonValue, name: str) -> list[str]:
    if not isinstance(value, list):
        raise SchemaDriftError(f"{name} must be a list")
    labels: list[str] = []
    for label in value:
        if not isinstance(label, str):
            raise SchemaDriftError(f"{name} must contain strings")
        labels.append(label)
    return labels


def _dates(value: JsonValue, name: str) -> list[date]:
    if not isinstance(value, list):
        raise SchemaDriftError(f"{name} must be a list")
    dates: list[date] = []
    for item in value:
        if not isinstance(item, str):
            raise SchemaDriftError(f"{name} must contain strings")
        try:
            dates.append(date.fromisoformat(item[:10]))
        except ValueError as error:
            raise SchemaDriftError(f"{name} contains a non-ISO date") from error
    return dates


def _value_rows(
    value: JsonValue, *, row_count: int, column_count: int, name: str
) -> list[list[JsonValue]]:
    if not isinstance(value, list) or len(value) != row_count:
        raise SchemaDriftError(f"{name} row count does not match labels")
    rows: list[list[JsonValue]] = []
    for row_index, raw_row in enumerate(value):
        if not isinstance(raw_row, list) or len(raw_row) != column_count:
            raise SchemaDriftError(f"{name} row {row_index} has the wrong width")
        rows.append(list(raw_row))
    return rows
