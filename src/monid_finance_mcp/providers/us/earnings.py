"""Financial Datasets EarningsRecord composition from DefiLlama.

Composes earnings records from the statements matrix and the filings
index: every record carries the six required Financial Datasets fields
(ticker, report_period, source_type, filing_date, filing_url,
accession_number) plus quarterly/annual financial blocks derived with
period-over-period and year-over-year changes.
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import date

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.filing_items import derive_accession
from monid_finance_mcp.providers.us.normalize import SchemaDriftError
from monid_finance_mcp.providers.us.statements import (
    PeriodRow,
    parse_statement_series,
)

EARNINGS_FORMS = ("10-K", "10-Q")

_QUARTERLY_LABELS = (
    "Revenue",
    "Gross Profit",
    "Operating Income",
    "Net Income",
    "EPS (Diluted)",
    "Shares Outstanding (Basic)",
    "Shares Outstanding (Diluted)",
    "Total Current Assets|Cash and Cash Equivalents",
    "Net Cash Flow",
    "Total Current Liabilities|Short-Term Debt",
    "Total Non-Current Liabilities|Long-Term Debt",
    "Total Assets",
    "Total Liabilities",
    "Total Shareholders Equity",
    "Cash Flow from Operating Activities",
    "Cash Flow from Investing Activities",
    "Cash Flow from Financing Activities",
    "Cash Flow from Investing Activities|Capital Expenditure",
    "Free Cash Flow",
)
_MARGIN_BASES = (
    ("gross_margin", "Gross Profit"),
    ("operating_margin", "Operating Income"),
    ("net_margin", "Net Income"),
)


@dataclass(frozen=True, slots=True)
class EarningsFiling:
    """One filing event selected for earnings composition."""

    report_period: date
    filing_date: date
    form: str
    filing_url: str
    accession_number: str


@dataclass(frozen=True, slots=True)
class EarningsData:
    records: list[JsonObject]
    fiscal_end_month: int | None


def normalize_earnings(
    statements_value: JsonValue,
    filings_rows: list[JsonObject],
    *,
    ticker: str,
    limit: int,
) -> EarningsData:
    """Compose EarningsRecord objects for 10-K and 10-Q filing events."""
    income = parse_statement_series(statements_value, "income")
    balance = parse_statement_series(statements_value, "balance")
    cash = parse_statement_series(statements_value, "cash")
    quarterly = _joined_by_date(income.quarterly, balance.quarterly, cash.quarterly)
    annual = _joined_by_date(income.annual, balance.annual, cash.annual)
    fiscal_end_month = _fiscal_end_month(income.annual)

    filings = _earnings_filings(filings_rows)
    filings.sort(key=lambda item: (item.filing_date, item.report_period), reverse=True)

    records: list[JsonObject] = []
    for filing in filings:
        quarter_values = quarterly.get(filing.report_period)
        if quarter_values is None:
            continue
        record: JsonObject = {
            "ticker": ticker,
            "report_period": filing.report_period.isoformat(),
        }
        fiscal_label = _fiscal_period_label(filing.report_period, fiscal_end_month)
        if fiscal_label is not None:
            record["fiscal_period"] = fiscal_label
        record["source_type"] = filing.form
        record["filing_date"] = filing.filing_date.isoformat()
        record["filing_url"] = filing.filing_url
        record["accession_number"] = filing.accession_number
        record["quarterly"] = _time_dimension(
            quarter_values,
            previous=_previous_quarter(quarterly, filing.report_period),
            year_over_year=_year_over_year_quarter(quarterly, filing.report_period),
            quarterly=True,
        )
        if filing.form == "10-K":
            annual_values = annual.get(filing.report_period)
            if annual_values is not None:
                record["annual"] = _time_dimension(
                    annual_values,
                    previous=_previous_year(annual, filing.report_period),
                    year_over_year=None,
                    quarterly=False,
                )
        records.append(record)
        if len(records) >= limit:
            break
    return EarningsData(records, fiscal_end_month)


def _joined_by_date(
    *series_lists: list[PeriodRow],
) -> dict[date, dict[str, JsonValue]]:
    maps: list[dict[date, dict[str, JsonValue]]] = []
    for rows in series_lists:
        maps.append({row.report_period: row.values for row in rows})
    dates = set(maps[0])
    for mapping in maps[1:]:
        dates &= set(mapping)
    joined: dict[date, dict[str, JsonValue]] = {}
    for day in dates:
        values: dict[str, JsonValue] = {}
        for mapping in maps:
            values.update(mapping[day])
        joined[day] = values
    return joined


def _earnings_filings(rows: list[JsonObject]) -> list[EarningsFiling]:
    filings: list[EarningsFiling] = []
    for row in rows:
        form = _opt_str(row.get("form"))
        if form is None or form.upper() not in EARNINGS_FORMS:
            continue
        report_period = _opt_date(row.get("report_date"))
        filing_date = _opt_date(row.get("filing_date"))
        url = _opt_str(row.get("primary_document_url"))
        if report_period is None or filing_date is None or url is None:
            continue
        accession = derive_accession(url)
        filings.append(
            EarningsFiling(
                report_period=report_period,
                filing_date=filing_date,
                form=form.upper(),
                filing_url=url,
                accession_number=accession or "",
            )
        )
    if not filings:
        raise SchemaDriftError("DefiLlama filings index has no 10-K or 10-Q rows")
    return filings


def _time_dimension(
    values: dict[str, JsonValue],
    *,
    previous: dict[str, JsonValue] | None,
    year_over_year: dict[str, JsonValue] | None,
    quarterly: bool,
) -> JsonObject:
    """One Financial Datasets EarningsTimeDimension block."""
    block: JsonObject = {}
    _put_change(block, "revenue", values, previous, year_over_year, quarterly=quarterly)
    _put_change(
        block, "earnings_per_share", values, previous, year_over_year, quarterly=quarterly
    )
    _put_change(block, "gross_profit", values, previous, year_over_year, quarterly=quarterly)
    for margin_name, base_label in _MARGIN_BASES:
        _put_margin(
            block, margin_name, values, previous, year_over_year, base_label,
            quarterly=quarterly,
        )
    _put_change(block, "operating_income", values, previous, year_over_year, quarterly=quarterly)
    _put_change(block, "net_income", values, previous, year_over_year, quarterly=quarterly)
    _put_value(block, "weighted_average_shares", values, "Shares Outstanding (Basic)")
    _put_value(
        block, "weighted_average_shares_diluted", values, "Shares Outstanding (Diluted)"
    )
    _put_value(
        block, "cash_and_equivalents", values, "Total Current Assets|Cash and Cash Equivalents"
    )
    _put_value(block, "change_in_cash_and_equivalents", values, "Net Cash Flow")
    short_debt = _num(values.get("Total Current Liabilities|Short-Term Debt"))
    long_debt = _num(values.get("Total Non-Current Liabilities|Long-Term Debt"))
    if short_debt is not None and long_debt is not None:
        block["total_debt"] = short_debt + long_debt
    _put_value(block, "total_assets", values, "Total Assets")
    _put_value(block, "total_liabilities", values, "Total Liabilities")
    _put_value(block, "shareholders_equity", values, "Total Shareholders Equity")
    _put_change(
        block, "net_cash_flow_from_operations", values, previous, year_over_year,
        quarterly=quarterly,
    )
    _put_change(
        block, "net_cash_flow_from_investing", values, previous, year_over_year,
        quarterly=quarterly,
    )
    _put_change(
        block, "net_cash_flow_from_financing", values, previous, year_over_year,
        quarterly=quarterly,
    )
    _put_change(block, "capital_expenditure", values, previous, year_over_year, quarterly=quarterly)
    _put_change(block, "free_cash_flow", values, previous, year_over_year, quarterly=quarterly)
    return block


def _put_change(
    block: JsonObject,
    name: str,
    values: dict[str, JsonValue],
    previous: dict[str, JsonValue] | None,
    year_over_year: dict[str, JsonValue] | None,
    *,
    quarterly: bool,
) -> None:
    """Set a value plus Financial Datasets change fields (decimal ratios)."""
    label = _LABEL_BY_FIELD[name]
    current = _num(values.get(label))
    if current is None:
        return
    block[name] = current
    prior = _num(previous.get(label)) if previous is not None else None
    if prior is not None and prior != 0:
        block[f"{name}_chg"] = (current - prior) / abs(prior)
    if quarterly:
        yoy = _num(year_over_year.get(label)) if year_over_year is not None else None
        if yoy is not None and yoy != 0:
            block[f"{name}_yoy_chg"] = (current - yoy) / abs(yoy)


def _put_margin(
    block: JsonObject,
    name: str,
    values: dict[str, JsonValue],
    previous: dict[str, JsonValue] | None,
    year_over_year: dict[str, JsonValue] | None,
    base_label: str,
    *,
    quarterly: bool,
) -> None:
    revenue = _num(values.get("Revenue"))
    base = _num(values.get(base_label))
    if revenue is None or revenue == 0 or base is None:
        return
    margin = base / revenue
    block[name] = margin
    prior_margin = _margin_of(previous, base_label) if previous is not None else None
    if prior_margin is not None:
        block[f"{name}_chg_bps"] = (margin - prior_margin) * 10_000
        if prior_margin != 0:
            block[f"{name}_chg_pct"] = (margin - prior_margin) / abs(prior_margin)
    if quarterly:
        yoy_margin = (
            _margin_of(year_over_year, base_label) if year_over_year is not None else None
        )
        if yoy_margin is not None:
            block[f"{name}_yoy_chg_bps"] = (margin - yoy_margin) * 10_000
            if yoy_margin != 0:
                block[f"{name}_yoy_chg_pct"] = (margin - yoy_margin) / abs(yoy_margin)


def _margin_of(values: dict[str, JsonValue] | None, base_label: str) -> float | None:
    if values is None:
        return None
    revenue = _num(values.get("Revenue"))
    base = _num(values.get(base_label))
    if revenue is None or revenue == 0 or base is None:
        return None
    return base / revenue


def _put_value(
    block: JsonObject, name: str, values: dict[str, JsonValue], label: str
) -> None:
    value = _num(values.get(label))
    if value is not None:
        block[name] = value


def _previous_quarter(
    quarterly: dict[date, dict[str, JsonValue]], day: date
) -> dict[str, JsonValue] | None:
    ordinal = day.year * 4 + (day.month - 1) // 3
    for candidate in quarterly:
        if candidate.year * 4 + (candidate.month - 1) // 3 == ordinal - 1:
            return quarterly[candidate]
    return None


def _year_over_year_quarter(
    quarterly: dict[date, dict[str, JsonValue]], day: date
) -> dict[str, JsonValue] | None:
    ordinal = day.year * 4 + (day.month - 1) // 3
    for candidate in quarterly:
        if candidate.year * 4 + (candidate.month - 1) // 3 == ordinal - 4:
            return quarterly[candidate]
    return None


def _previous_year(
    annual: dict[date, dict[str, JsonValue]], day: date
) -> dict[str, JsonValue] | None:
    for candidate in annual:
        if candidate.year == day.year - 1:
            return annual[candidate]
    return None


def _fiscal_end_month(annual: list[PeriodRow]) -> int | None:
    months = {row.report_period.month for row in annual}
    return months.pop() if len(months) == 1 else None


def _fiscal_period_label(day: date, year_end_month: int | None) -> str | None:
    if year_end_month is None:
        return None
    year = day.year
    if day.month > year_end_month:
        year += 1
    quarter = ((day.month - 1) // 3) + 1
    return f"{year}-Q{quarter}"


def _num(value: JsonValue) -> float | None:
    if isinstance(value, bool) or not isinstance(value, int | float):
        return None
    return float(value)


def _opt_str(value: JsonValue) -> str | None:
    return value if isinstance(value, str) and value else None


def _opt_date(value: JsonValue) -> date | None:
    text = _opt_str(value)
    if text is None:
        return None
    try:
        return date.fromisoformat(text[:10])
    except ValueError:
        return None


_LABEL_BY_FIELD: dict[str, str] = {
    "revenue": "Revenue",
    "earnings_per_share": "EPS (Diluted)",
    "gross_profit": "Gross Profit",
    "operating_income": "Operating Income",
    "net_income": "Net Income",
    "net_cash_flow_from_operations": "Cash Flow from Operating Activities",
    "net_cash_flow_from_investing": "Cash Flow from Investing Activities",
    "net_cash_flow_from_financing": "Cash Flow from Financing Activities",
    "capital_expenditure": "Cash Flow from Investing Activities|Capital Expenditure",
    "free_cash_flow": "Free Cash Flow",
}


@dataclass(frozen=True, slots=True)
class EarningsCalendarEntry:
    """One Nasdaq earnings-calendar reporter, for the market-wide feed."""

    ticker: str
    report_date: date | None
    filing_date: date | None


def parse_earnings_calendar(value: JsonValue, *, limit: int) -> list[EarningsCalendarEntry]:
    """Parse Nasdaq /get_earnings_calendar rows into unique reporters.

    Rows are ordered most-recent first by filing date, then report date;
    a ticker appears once. An unrecognizable payload raises
    SchemaDriftError so the caller answers with an honest fd_error.
    """
    raw_rows = _calendar_rows(value)
    entries: list[EarningsCalendarEntry] = []
    for index, row in enumerate(raw_rows):
        if not isinstance(row, dict):
            raise SchemaDriftError(f"Nasdaq earnings calendar row[{index}] must be an object")
        ticker = _opt_str(row.get("ticker")) or _opt_str(row.get("symbol"))
        if ticker is None:
            continue
        entries.append(
            EarningsCalendarEntry(
                ticker=ticker.upper(),
                report_date=_opt_date(row.get("reportDate") or row.get("report_date")
                                      or row.get("periodEnding") or row.get("date")),
                filing_date=_opt_date(row.get("filingDate") or row.get("filing_date")),
            )
        )
    if not entries:
        return []

    def sort_key(entry: EarningsCalendarEntry) -> tuple[bool, date, bool, date, str]:
        return (
            entry.filing_date is not None,
            entry.filing_date or date.min,
            entry.report_date is not None,
            entry.report_date or date.min,
            entry.ticker,
        )

    entries.sort(key=sort_key, reverse=True)
    unique: list[EarningsCalendarEntry] = []
    seen: set[str] = set()
    for entry in entries:
        if entry.ticker in seen:
            continue
        seen.add(entry.ticker)
        unique.append(entry)
        if len(unique) >= limit:
            break
    return unique


def _calendar_rows(value: JsonValue) -> list[JsonValue]:
    current = value
    for _ in range(5):
        if isinstance(current, list):
            return current
        if not isinstance(current, dict):
            break
        child: JsonValue | None = None
        for key in ("rows", "results", "data", "earnings", "calendar"):
            candidate = current.get(key)
            if isinstance(candidate, list | dict):
                child = candidate
                break
        if child is None:
            break
        current = child
    raise SchemaDriftError("Nasdaq earnings calendar payload is not parseable")
