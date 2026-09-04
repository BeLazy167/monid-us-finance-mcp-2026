from __future__ import annotations

import math
from dataclasses import dataclass
from datetime import date
from typing import Literal

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import SchemaDriftError

type MetricsPeriod = Literal["annual", "quarterly", "ttm"]
type NumericValues = dict[str, int | float]

# Financial Datasets valuation fields (enterprise_value, price_to_* ratios,
# EV multiples, free_cash_flow_yield, peg_ratio, return_on_invested_capital,
# currency, filing_datetime) are omitted: the validated routes cannot source
# them per historical period without fabricating data.

_INCOME = "income"
_BALANCE = "balance"
_CASH = "cash"


def _top(statement: str, label: str) -> str:
    return f"{statement}|top|{label}"


def _child(statement: str, parent: str, label: str) -> str:
    return f"{statement}|{parent}|{label}"


REVENUE = _top(_INCOME, "Revenue")
COST_OF_REVENUE = _top(_INCOME, "Cost of Revenue")
GROSS_PROFIT = _top(_INCOME, "Gross Profit")
OPERATING_INCOME = _top(_INCOME, "Operating Income")
NET_INCOME = _top(_INCOME, "Net Income")
DILUTED_EPS = _top(_INCOME, "EPS (Diluted)")
EBIT = _top(_INCOME, "EBIT")
EBITDA = _top(_INCOME, "EBITDA")
INTEREST_EXPENSE = _child(
    _INCOME, "Non-Operating Items", "Non-Operating Interest Expense"
)
CURRENT_ASSETS = _top(_BALANCE, "Total Current Assets")
CURRENT_LIABILITIES = _top(_BALANCE, "Total Current Liabilities")
TOTAL_ASSETS = _top(_BALANCE, "Total Assets")
SHAREHOLDERS_EQUITY = _top(_BALANCE, "Total Shareholders Equity")
CASH_AND_EQUIVALENTS = _child(
    _BALANCE, "Total Current Assets", "Cash and Cash Equivalents"
)
ACCOUNTS_RECEIVABLE = _child(_BALANCE, "Total Current Assets", "Accounts Receivable")
INVENTORY = _child(_BALANCE, "Total Current Assets", "Inventory")
SHORT_TERM_DEBT = _child(_BALANCE, "Total Current Liabilities", "Short-Term Debt")
LONG_TERM_DEBT = _child(
    _BALANCE, "Total Non-Current Liabilities", "Long-Term Debt"
)
OPERATING_CASH_FLOW = _top(_CASH, "Cash Flow from Operating Activities")
FREE_CASH_FLOW = _top(_CASH, "Free Cash Flow")
SHARES_OUTSTANDING = _top(_INCOME, "Shares Outstanding (Basic)")
COMMON_DIVIDENDS = _child(
    _CASH, "Cash Flow from Financing Activities", "Common Dividends"
)

_FLOW_FIELDS = (
    REVENUE,
    COST_OF_REVENUE,
    GROSS_PROFIT,
    OPERATING_INCOME,
    NET_INCOME,
    DILUTED_EPS,
    EBIT,
    EBITDA,
    INTEREST_EXPENSE,
    OPERATING_CASH_FLOW,
    FREE_CASH_FLOW,
    COMMON_DIVIDENDS,
)
_BALANCE_FIELDS = (
    CURRENT_ASSETS,
    CURRENT_LIABILITIES,
    TOTAL_ASSETS,
    SHAREHOLDERS_EQUITY,
    CASH_AND_EQUIVALENTS,
    ACCOUNTS_RECEIVABLE,
    INVENTORY,
    SHORT_TERM_DEBT,
    LONG_TERM_DEBT,
)
_GROWTH_OUTPUTS = {
    "revenue_growth": REVENUE,
    "earnings_growth": NET_INCOME,
    "book_value_growth": SHAREHOLDERS_EQUITY,
    "earnings_per_share_growth": DILUTED_EPS,
    "free_cash_flow_growth": FREE_CASH_FLOW,
    "operating_income_growth": OPERATING_INCOME,
    "ebitda_growth": EBITDA,
}


@dataclass(frozen=True, slots=True)
class _PeriodRow:
    report_period: date
    values: NumericValues


@dataclass(frozen=True, slots=True)
class MetricsData:
    records: list[JsonObject]
    as_of: str | None
    incomplete_ttm_windows: int = 0
    fiscal_end_month: int | None = None


def normalize_financial_metrics(
    value: JsonValue,
    *,
    ticker: str,
    period: MetricsPeriod,
    limit: int,
    report_period: date | None,
    report_period_gte: date | None,
    report_period_lte: date | None,
    report_period_gt: date | None,
    report_period_lt: date | None,
) -> MetricsData:
    root = _statement_root(value)
    annual = _joined_rows(root, "annual")
    quarterly = _joined_rows(root, "quarterly")
    fiscal_end_month = _fiscal_year_end_month(annual)

    incomplete_windows = 0
    if period == "annual":
        raw_rows = _base_metric_rows(
            annual, ticker=ticker, period=period, annual=True, fiscal_end_month=fiscal_end_month
        )
    elif period == "quarterly":
        raw_rows = _base_metric_rows(
            quarterly, ticker=ticker, period=period, annual=False, fiscal_end_month=fiscal_end_month
        )
    else:
        raw_rows, incomplete_windows = _ttm_metric_rows(quarterly, ticker, fiscal_end_month)

    filtered = [
        record
        for record in raw_rows
        if _date_matches(
            _record_date(record),
            exact=report_period,
            minimum=report_period_gte,
            maximum=report_period_lte,
            greater=report_period_gt,
            less=report_period_lt,
        )
    ]
    filtered.sort(key=_record_date, reverse=True)
    selected = filtered[:limit]
    as_of = selected[0].get("report_period") if selected else None
    return MetricsData(
        selected,
        as_of if isinstance(as_of, str) else None,
        incomplete_ttm_windows=incomplete_windows,
        fiscal_end_month=fiscal_end_month,
    )


def _base_metric_rows(
    rows: list[_PeriodRow],
    *,
    ticker: str,
    period: MetricsPeriod,
    annual: bool,
    fiscal_end_month: int | None,
) -> list[JsonObject]:
    if annual:

        def previous_key(item: _PeriodRow) -> int:
            return item.report_period.year - 1

        growth_key = previous_key
        by_key = _unique_by_year(rows)
    else:

        def previous_key(item: _PeriodRow) -> int:
            return _quarter_ordinal(item.report_period) - 1

        def growth_key(item: _PeriodRow) -> int:
            return _quarter_ordinal(item.report_period) - 4

        by_key = _unique_by_quarter(rows)

    result: list[JsonObject] = []
    for row in rows:
        previous = by_key.get(previous_key(row))
        growth_prior = by_key.get(growth_key(row))
        result.append(
            _metric_record(
                ticker=ticker,
                period=period,
                row=row,
                previous_balance=previous,
                growth_prior=growth_prior,
                fiscal_period=_fiscal_period_label(
                    row, fiscal_end_month, annual=annual, ttm=False
                ),
            )
        )
    return result


def _ttm_metric_rows(
    quarterly: list[_PeriodRow], ticker: str, fiscal_end_month: int | None
) -> tuple[list[JsonObject], int]:
    by_quarter = _unique_by_quarter(quarterly)
    if not by_quarter:
        return [], 0
    first = min(by_quarter)
    ttm_rows: dict[int, _PeriodRow] = {}
    incomplete = 0
    for ordinal in sorted(by_quarter):
        if ordinal < first + 3:
            continue
        window = [by_quarter.get(value) for value in range(ordinal - 3, ordinal + 1)]
        if any(item is None for item in window):
            incomplete += 1
            continue
        complete_window = [item for item in window if item is not None]
        ending = complete_window[-1]
        values: NumericValues = {}
        for field in _FLOW_FIELDS:
            operands = [item.values.get(field) for item in complete_window]
            if all(operand is not None for operand in operands):
                values[field] = sum(operand for operand in operands if operand is not None)
        for field in _BALANCE_FIELDS:
            operand = ending.values.get(field)
            if operand is not None:
                values[field] = operand
        ttm_rows[ordinal] = _PeriodRow(ending.report_period, values)

    result: list[JsonObject] = []
    for ordinal, row in ttm_rows.items():
        result.append(
            _metric_record(
                ticker=ticker,
                period="ttm",
                row=row,
                previous_balance=by_quarter.get(ordinal - 1),
                growth_prior=ttm_rows.get(ordinal - 4),
                fiscal_period=None,
            )
        )
    return result, incomplete


def _metric_record(
    *,
    ticker: str,
    period: MetricsPeriod,
    row: _PeriodRow,
    previous_balance: _PeriodRow | None,
    growth_prior: _PeriodRow | None,
    fiscal_period: str | None,
) -> JsonObject:
    """One Financial Datasets financial-metrics record, FD key order."""
    values = row.values
    record: JsonObject = {
        "ticker": ticker,
        "report_period": row.report_period.isoformat(),
    }
    if fiscal_period is not None:
        record["fiscal_period"] = fiscal_period
    record["period"] = period

    _put(record, "gross_margin", _ratio(values.get(GROSS_PROFIT), values.get(REVENUE)))
    _put(record, "operating_margin", _ratio(values.get(OPERATING_INCOME), values.get(REVENUE)))
    _put(record, "net_margin", _ratio(values.get(NET_INCOME), values.get(REVENUE)))
    _put(
        record,
        "return_on_equity",
        _ratio(values.get(NET_INCOME), values.get(SHAREHOLDERS_EQUITY)),
    )
    _put(record, "return_on_assets", _ratio(values.get(NET_INCOME), values.get(TOTAL_ASSETS)))

    if previous_balance is not None:
        previous = previous_balance.values
        _put(
            record,
            "asset_turnover",
            _turnover(values.get(REVENUE), values.get(TOTAL_ASSETS), previous.get(TOTAL_ASSETS)),
        )
        _put(
            record,
            "inventory_turnover",
            _turnover(
                values.get(COST_OF_REVENUE),
                values.get(INVENTORY),
                previous.get(INVENTORY),
            ),
        )
        _put(
            record,
            "receivables_turnover",
            _turnover(
                values.get(REVENUE),
                values.get(ACCOUNTS_RECEIVABLE),
                previous.get(ACCOUNTS_RECEIVABLE),
            ),
        )
        average_receivables = _average(
            values.get(ACCOUNTS_RECEIVABLE), previous.get(ACCOUNTS_RECEIVABLE)
        )
        _put(record, "days_sales_outstanding", _days(values.get(REVENUE), average_receivables))
        average_inventory = _average(values.get(INVENTORY), previous.get(INVENTORY))
        inventory_turnover = _ratio(values.get(COST_OF_REVENUE), average_inventory)
        receivables_turnover = _ratio(values.get(REVENUE), average_receivables)
        if inventory_turnover is not None and receivables_turnover is not None:
            record["operating_cycle"] = inventory_turnover + receivables_turnover
        current_working_capital = _difference(
            values.get(CURRENT_ASSETS), values.get(CURRENT_LIABILITIES)
        )
        previous_working_capital = _difference(
            previous.get(CURRENT_ASSETS), previous.get(CURRENT_LIABILITIES)
        )
        _put(
            record,
            "working_capital_turnover",
            _turnover(values.get(REVENUE), current_working_capital, previous_working_capital),
        )

    _put(
        record,
        "current_ratio",
        _ratio(values.get(CURRENT_ASSETS), values.get(CURRENT_LIABILITIES)),
    )
    quick_assets = _difference(values.get(CURRENT_ASSETS), values.get(INVENTORY))
    _put(record, "quick_ratio", _ratio(quick_assets, values.get(CURRENT_LIABILITIES)))
    _put(
        record,
        "cash_ratio",
        _ratio(values.get(CASH_AND_EQUIVALENTS), values.get(CURRENT_LIABILITIES)),
    )
    _put(
        record,
        "operating_cash_flow_ratio",
        _ratio(values.get(OPERATING_CASH_FLOW), values.get(CURRENT_LIABILITIES)),
    )

    short_debt = values.get(SHORT_TERM_DEBT)
    long_debt = values.get(LONG_TERM_DEBT)
    total_debt = (
        short_debt + long_debt
        if short_debt is not None and long_debt is not None
        else None
    )
    _put(record, "debt_to_equity", _ratio(total_debt, values.get(SHAREHOLDERS_EQUITY)))
    _put(record, "debt_to_assets", _ratio(total_debt, values.get(TOTAL_ASSETS)))
    _put(record, "interest_coverage", _ratio(values.get(EBIT), values.get(INTEREST_EXPENSE)))
    common_dividends = values.get(COMMON_DIVIDENDS)
    _put(
        record,
        "payout_ratio",
        _ratio(
            abs(common_dividends) if common_dividends is not None else None,
            values.get(NET_INCOME),
        ),
    )

    if growth_prior is not None:
        for output, field in _GROWTH_OUTPUTS.items():
            _put(record, output, _growth(values.get(field), growth_prior.values.get(field)))

    diluted_eps = values.get(DILUTED_EPS)
    if diluted_eps is not None:
        record["earnings_per_share"] = diluted_eps
    _put(
        record,
        "book_value_per_share",
        _ratio(values.get(SHAREHOLDERS_EQUITY), values.get(SHARES_OUTSTANDING)),
    )
    _put(
        record,
        "free_cash_flow_per_share",
        _ratio(values.get(FREE_CASH_FLOW), values.get(SHARES_OUTSTANDING)),
    )
    return record


def _joined_rows(root: JsonObject, period: Literal["annual", "quarterly"]) -> list[_PeriodRow]:
    income = _section_rows(
        _required_section(root, "incomeStatement"), _INCOME, period
    )
    balance = _section_rows(
        _required_section(root, "balanceSheet"), _BALANCE, period
    )
    cash = _section_rows(_required_section(root, "cashflow"), _CASH, period)
    dates = set(income) & set(balance) & set(cash)
    rows: list[_PeriodRow] = []
    for report_period in sorted(dates):
        merged = dict(income[report_period])
        merged.update(balance[report_period])
        merged.update(cash[report_period])
        rows.append(_PeriodRow(report_period, merged))
    return rows


def _section_rows(
    section: JsonObject, statement: str, period: Literal["annual", "quarterly"]
) -> dict[date, NumericValues]:
    labels = _labels(section.get("labels"), f"DefiLlama {statement} labels")
    children_value = section.get("children")
    children = children_value if isinstance(children_value, dict) else {}
    definitions_value = children.get(period)
    child_definitions = definitions_value if isinstance(definitions_value, dict) else {}
    block = _object(section.get(period), f"DefiLlama {statement}.{period}")
    dates = _dates(
        block.get("periodEnding"), f"DefiLlama {statement}.{period}.periodEnding"
    )
    values = _value_rows(
        block.get("values"),
        row_count=len(labels),
        column_count=len(dates),
        name=f"DefiLlama {statement}.{period}.values",
    )
    rows: dict[date, NumericValues] = {report_period: {} for report_period in dates}
    for row_index, label in enumerate(labels):
        key = _top(statement, label)
        for column, report_period in enumerate(dates):
            operand = values[row_index][column]
            if operand is not None:
                rows[report_period][key] = operand

    block_children_value = block.get("children")
    block_children = block_children_value if isinstance(block_children_value, dict) else {}
    if set(block_children) != set(child_definitions):
        raise SchemaDriftError(
            f"DefiLlama {statement}.{period} child definitions and values differ"
        )
    for parent, definition_value in child_definitions.items():
        definition = _object(
            definition_value, f"DefiLlama {statement}.{period}.{parent} definition"
        )
        child_labels = _labels(
            definition.get("labels"), f"DefiLlama {statement}.{period}.{parent} labels"
        )
        child_block = _object(
            block_children.get(parent), f"DefiLlama {statement}.{period}.{parent}"
        )
        child_values = _value_rows(
            child_block.get("values"),
            row_count=len(child_labels),
            column_count=len(dates),
            name=f"DefiLlama {statement}.{period}.{parent}.values",
        )
        for row_index, label in enumerate(child_labels):
            key = _child(statement, parent, label)
            for column, report_period in enumerate(dates):
                operand = child_values[row_index][column]
                if operand is not None:
                    rows[report_period][key] = operand
    return rows


def _statement_root(value: JsonValue) -> JsonObject:
    root = _object(value, "DefiLlama statements payload")
    if "incomeStatement" not in root and isinstance(root.get("data"), dict):
        root = root["data"]
        if not isinstance(root, dict):
            raise SchemaDriftError("DefiLlama statements data must be an object")
    return root


def _required_section(root: JsonObject, key: str) -> JsonObject:
    return _object(root.get(key), f"DefiLlama statements {key}")


def _labels(value: JsonValue | None, name: str) -> list[str]:
    if not isinstance(value, list):
        raise SchemaDriftError(f"{name} must be an array")
    result: list[str] = []
    for index, item in enumerate(value):
        if not isinstance(item, str) or not item:
            raise SchemaDriftError(f"{name}[{index}] must be a non-empty string")
        result.append(item)
    if len(set(result)) != len(result):
        raise SchemaDriftError(f"{name} contains ambiguous duplicate labels")
    return result


def _dates(value: JsonValue | None, name: str) -> list[date]:
    if not isinstance(value, list):
        raise SchemaDriftError(f"{name} must be an array")
    result: list[date] = []
    for index, item in enumerate(value):
        if not isinstance(item, str):
            raise SchemaDriftError(f"{name}[{index}] must be an ISO date string")
        try:
            parsed = date.fromisoformat(item)
        except ValueError as error:
            raise SchemaDriftError(f"{name}[{index}] contains an invalid date") from error
        if parsed.isoformat() != item:
            raise SchemaDriftError(f"{name}[{index}] must use YYYY-MM-DD")
        result.append(parsed)
    if len(set(result)) != len(result):
        raise SchemaDriftError(f"{name} contains duplicate periods")
    return result


def _value_rows(
    value: JsonValue | None, *, row_count: int, column_count: int, name: str
) -> list[list[int | float | None]]:
    if not isinstance(value, list) or len(value) != row_count:
        raise SchemaDriftError(f"{name} has the wrong row count")
    result: list[list[int | float | None]] = []
    for row_index, raw_row in enumerate(value):
        if not isinstance(raw_row, list) or len(raw_row) != column_count:
            raise SchemaDriftError(f"{name}[{row_index}] has the wrong width")
        row: list[int | float | None] = []
        for column, operand in enumerate(raw_row):
            if operand is None:
                row.append(None)
            elif (
                isinstance(operand, bool)
                or not isinstance(operand, int | float)
                or not math.isfinite(operand)
            ):
                raise SchemaDriftError(
                    f"{name}[{row_index}][{column}] must be finite numeric data or null"
                )
            else:
                row.append(operand)
        result.append(row)
    return result


def _unique_by_year(rows: list[_PeriodRow]) -> dict[int, _PeriodRow]:
    result: dict[int, _PeriodRow] = {}
    for row in rows:
        year = row.report_period.year
        if year in result:
            raise SchemaDriftError("DefiLlama annual statements contain two periods in one year")
        result[year] = row
    return result


def _unique_by_quarter(rows: list[_PeriodRow]) -> dict[int, _PeriodRow]:
    result: dict[int, _PeriodRow] = {}
    for row in rows:
        ordinal = _quarter_ordinal(row.report_period)
        if ordinal in result:
            raise SchemaDriftError(
                "DefiLlama quarterly statements contain two periods in one quarter"
            )
        result[ordinal] = row
    return result


def _quarter_ordinal(value: date) -> int:
    return value.year * 4 + (value.month - 1) // 3


def _ratio(
    numerator: int | float | None, denominator: int | float | None
) -> float | None:
    if numerator is None or denominator is None or denominator == 0:
        return None
    return numerator / denominator


def _turnover(
    numerator: int | float | None,
    current_balance: int | float | None,
    previous_balance: int | float | None,
) -> float | None:
    if numerator is None or current_balance is None or previous_balance is None:
        return None
    average = (current_balance + previous_balance) / 2
    return None if average == 0 else numerator / average


def _growth(
    current: int | float | None, previous: int | float | None
) -> float | None:
    if current is None or previous is None or previous == 0:
        return None
    return (current - previous) / abs(previous)


def _difference(
    minuend: int | float | None, subtrahend: int | float | None
) -> int | float | None:
    if minuend is None or subtrahend is None:
        return None
    return minuend - subtrahend


def _put(record: JsonObject, key: str, value: int | float | None) -> None:
    if value is not None and math.isfinite(value):
        record[key] = value


def _average(
    current: int | float | None, previous: int | float | None
) -> int | float | None:
    if current is None or previous is None:
        return None
    return (current + previous) / 2


def _days(
    revenue: int | float | None, average_receivables: int | float | None
) -> float | None:
    if revenue is None or average_receivables is None or revenue == 0:
        return None
    return 365 * average_receivables / revenue


def _record_date(record: JsonObject) -> date:
    value = record.get("report_period")
    if not isinstance(value, str):
        raise RuntimeError("Normalized financial metric omitted report_period")
    return date.fromisoformat(value)


def _date_matches(
    value: date,
    *,
    exact: date | None,
    minimum: date | None,
    maximum: date | None,
    greater: date | None,
    less: date | None,
) -> bool:
    if exact is not None and value != exact:
        return False
    if minimum is not None and value < minimum:
        return False
    if maximum is not None and value > maximum:
        return False
    if greater is not None and value <= greater:
        return False
    return less is None or value < less


def _fiscal_year_end_month(annual: list[_PeriodRow]) -> int | None:
    months = {row.report_period.month for row in annual}
    return months.pop() if len(months) == 1 else None


def _fiscal_period_label(
    row: _PeriodRow, year_end_month: int | None, *, annual: bool, ttm: bool
) -> str | None:
    if ttm:
        return None
    if annual:
        return f"FY{row.report_period.year}"
    if year_end_month is None:
        return None
    year = row.report_period.year
    if row.report_period.month > year_end_month:
        year += 1
    quarter = ((row.report_period.month - 1) // 3) + 1
    return f"{year}-Q{quarter}"


def _object(value: JsonValue | None, name: str) -> JsonObject:
    if not isinstance(value, dict):
        raise SchemaDriftError(f"{name} must be an object")
    return value
