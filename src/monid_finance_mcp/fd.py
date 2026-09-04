"""Financial Datasets API response record builders.

Every function here emits ONLY keys defined by the Financial Datasets
OpenAPI schema, in schema property order. Fields without a sourced value
are omitted (all such fields are optional or nullable upstream).
"""
from __future__ import annotations

from dataclasses import dataclass
from datetime import date

from monid_finance_mcp.models import JsonObject, JsonValue

FILING_TYPES = ("10-K", "10-Q", "8-K", "20-F", "6-K")


@dataclass(frozen=True, slots=True)
class FilingIdentity:
    """Filing metadata joined onto a statement record, when available."""

    accession_number: str | None
    form_type: str | None
    filing_url: str | None
    filing_date: date | None


def _set(record: JsonObject, key: str, value: JsonValue) -> None:
    """Set an optional FD field only when a sourced value exists."""
    if value is None:
        return
    record[key] = value


def _number(value: JsonValue) -> JsonValue:
    return value if isinstance(value, int | float) and not isinstance(value, bool) else None


def _clean_number(value: int | float | None) -> int | float | None:
    """Return integral floats as ints and drop representation noise."""
    if value is None:
        return None
    if isinstance(value, float) and value.is_integer():
        return int(value)
    return value


def company_facts_response(
    *, ticker: str, name: str | None, sector: str | None, industry: str | None, exchange: str | None
) -> JsonObject:
    facts: JsonObject = {"ticker": ticker}
    _set(facts, "name", name)
    _set(facts, "industry", industry)
    _set(facts, "sector", sector)
    _set(facts, "exchange", exchange)
    return {"company_facts": facts}


def _identity_fields(record: JsonObject, identity: FilingIdentity) -> None:
    _set(record, "accession_number", identity.accession_number)
    _set(record, "form_type", identity.form_type)
    _set(record, "filing_url", identity.filing_url)
    filing_day = identity.filing_date.isoformat() if identity.filing_date else None
    _set(record, "filing_date", filing_day)


def _statement_header(
    *,
    ticker: str,
    period: str,
    report_period: str,
    fiscal_period: str | None,
    identity: FilingIdentity | None,
) -> JsonObject:
    record: JsonObject = {"ticker": ticker, "report_period": report_period}
    _set(record, "fiscal_period", fiscal_period)
    record["period"] = period
    if identity is not None:
        _identity_fields(record, identity)
    return record


def income_statement_record(
    *,
    ticker: str,
    period: str,
    report_period: str,
    fiscal_period: str | None,
    values: dict[str, JsonValue],
    identity: FilingIdentity | None,
) -> JsonObject:
    record = _statement_header(
        ticker=ticker,
        period=period,
        report_period=report_period,
        fiscal_period=fiscal_period,
        identity=identity,
    )
    _put_fields(record, values, _INCOME_FIELDS)
    return record


def balance_sheet_record(
    *,
    ticker: str,
    period: str,
    report_period: str,
    fiscal_period: str | None,
    values: dict[str, JsonValue],
    identity: FilingIdentity | None,
) -> JsonObject:
    record = _statement_header(
        ticker=ticker,
        period=period,
        report_period=report_period,
        fiscal_period=fiscal_period,
        identity=identity,
    )
    _put_fields(record, values, _BALANCE_FIELDS)
    return record


def cash_flow_record(
    *,
    ticker: str,
    period: str,
    report_period: str,
    fiscal_period: str | None,
    values: dict[str, JsonValue],
    identity: FilingIdentity | None,
) -> JsonObject:
    record = _statement_header(
        ticker=ticker,
        period=period,
        report_period=report_period,
        fiscal_period=fiscal_period,
        identity=identity,
    )
    _put_fields(record, values, _CASH_FIELDS)
    return record


def filing_record(
    *,
    ticker: str,
    report_date: str | None,
    filing_date: str | None,
    form: str | None,
    url: str | None,
    accession_number: str | None,
) -> JsonObject:
    record: JsonObject = {}
    _set(record, "accession_number", accession_number)
    _set(record, "filing_type", form)
    _set(record, "report_date", report_date)
    _set(record, "filing_date", filing_date)
    record["ticker"] = ticker
    _set(record, "url", url)
    return record


def price_record(
    *, day: str, open_: float, high: float, low: float, close: float, volume: float
) -> JsonObject:
    integral_volume = int(volume) if float(volume).is_integer() else volume
    return {
        "open": open_,
        "close": close,
        "high": high,
        "low": low,
        "volume": integral_volume,
        "time": day,
    }


def price_snapshot_response(
    *,
    ticker: str,
    price: float,
    day_change: float | None,
    day_change_percent: float | None,
    time: str | None,
) -> JsonObject:
    snapshot: JsonObject = {"price": price, "ticker": ticker}
    _set(snapshot, "day_change", _number(day_change))
    _set(snapshot, "day_change_percent", _number(day_change_percent))
    _set(snapshot, "time", time)
    return {"snapshot": snapshot}


def metric_snapshot_record(
    *,
    ticker: str,
    market_cap: float | None,
    enterprise_value: float | None,
    trailing_pe: float | None,
    price_to_book: float | None,
    price_to_revenue: float | None,
    ev_to_ebitda: float | None,
    gross_margin_ttm: float | None,
    operating_margin_ttm: float | None,
    net_margin_ttm: float | None,
) -> JsonObject:
    record: JsonObject = {"ticker": ticker}
    _set(record, "market_cap", _number(market_cap))
    _set(record, "enterprise_value", _number(enterprise_value))
    _set(record, "price_to_earnings_ratio", _number(trailing_pe))
    _set(record, "price_to_book_ratio", _number(price_to_book))
    _set(record, "price_to_sales_ratio", _number(price_to_revenue))
    _set(record, "enterprise_value_to_ebitda_ratio", _number(ev_to_ebitda))
    _set(record, "gross_margin", _number(gross_margin_ttm))
    _set(record, "operating_margin", _number(operating_margin_ttm))
    _set(record, "net_margin", _number(net_margin_ttm))
    return {"snapshot": record}


def news_record(
    *, ticker: str, title: str | None, source: str | None, date_: str | None, url: str | None
) -> JsonObject:
    record: JsonObject = {"ticker": ticker}
    _set(record, "title", title)
    _set(record, "source", source)
    _set(record, "date", date_)
    _set(record, "url", url)
    return record


def filing_items_response(
    *,
    resource: str,
    ticker: str,
    filing_type: str,
    accession_number: str | None,
    year: int,
    quarter: int | None,
    items: list[JsonObject],
) -> JsonObject:
    return {
        "resource": resource,
        "ticker": ticker,
        "filing_type": filing_type,
        "accession_number": accession_number,
        "year": year,
        "quarter": quarter,
        "items": items,
    }


def filing_item_record(*, number: str, name: str, text: str) -> JsonObject:
    return {"number": number, "name": name, "text": text}


def list_response(key: str, records: list[JsonObject], next_url: str | None) -> JsonObject:
    response: JsonObject = {key: records}
    if next_url is not None:
        response["next_page_url"] = next_url
    return response


def prices_response(ticker: str, records: list[JsonObject], next_url: str | None) -> JsonObject:
    response: JsonObject = {"ticker": ticker, "prices": records}
    if next_url is not None:
        response["next_page_url"] = next_url
    return response


def _put_fields(
    record: JsonObject, values: dict[str, JsonValue], fields: tuple[tuple[str, str], ...]
) -> None:
    """Copy sourced statement values into FD fields in schema order."""
    for fd_field, source_key in fields:
        _set(record, fd_field, _number(values.get(source_key)))


# (fd_field, statements.py source key) pairs, in Financial Datasets schema order.
_INCOME_FIELDS: tuple[tuple[str, str], ...] = (
    ("revenue", "Revenue"),
    ("cost_of_revenue", "Cost of Revenue"),
    ("gross_profit", "Gross Profit"),
    ("operating_expense", "Operating Expenses"),
    (
        "selling_general_and_administrative_expenses",
        "Operating Expenses|Selling, General & Administrative",
    ),
    ("research_and_development", "Operating Expenses|Research and Development"),
    ("operating_income", "Operating Income"),
    ("interest_expense", "Non-Operating Items|Non-Operating Interest Expense"),
    ("ebit", "EBIT"),
    ("income_tax_expense", "Income Tax"),
    ("net_income", "Net Income"),
    ("net_income_common_stock", "Net Income to Common"),
    ("earnings_per_share", "EPS (Basic)"),
    ("earnings_per_share_diluted", "EPS (Diluted)"),
    ("weighted_average_shares", "Shares Outstanding (Basic)"),
    ("weighted_average_shares_diluted", "Shares Outstanding (Diluted)"),
)

_BALANCE_FIELDS: tuple[tuple[str, str], ...] = (
    ("total_assets", "Total Assets"),
    ("current_assets", "Total Current Assets"),
    ("cash_and_equivalents", "Total Current Assets|Cash and Cash Equivalents"),
    ("inventory", "Total Current Assets|Inventory"),
    ("trade_and_non_trade_receivables", "Total Current Assets|Accounts Receivable"),
    ("non_current_assets", "Total Non-Current Assets"),
    ("property_plant_and_equipment", "Total Non-Current Assets|Property, Plant & Equipment"),
    ("goodwill_and_intangible_assets", "Total Non-Current Assets|Goodwill and Intangible Assets"),
    ("outstanding_shares", "Shares Outstanding (Basic)"),
    ("total_liabilities", "Total Liabilities"),
    ("current_liabilities", "Total Current Liabilities"),
    ("current_debt", "Total Current Liabilities|Short-Term Debt"),
    ("non_current_liabilities", "Total Non-Current Liabilities"),
    ("non_current_debt", "Total Non-Current Liabilities|Long-Term Debt"),
    ("shareholders_equity", "Total Shareholders Equity"),
    ("retained_earnings", "Total Shareholders Equity|Retained Earnings"),
    ("total_debt", "Debt"),
)

_CASH_FIELDS: tuple[tuple[str, str], ...] = (
    ("net_income", "Net Income"),
    ("depreciation_and_amortization", "Depreciation and Amortization"),
    ("share_based_compensation", "Cash Flow from Operating Activities|Share-Based Compensation"),
    ("net_cash_flow_from_operations", "Cash Flow from Operating Activities"),
    ("capital_expenditure", "Cash Flow from Investing Activities|Capital Expenditure"),
    ("net_cash_flow_from_investing", "Cash Flow from Investing Activities"),
    (
        "dividends_and_other_cash_distributions",
        "Cash Flow from Financing Activities|Common Dividends",
    ),
    ("net_cash_flow_from_financing", "Cash Flow from Financing Activities"),
    ("change_in_cash_and_equivalents", "Net Cash Flow"),
    ("ending_cash_balance", "End Cash Position"),
    ("free_cash_flow", "Free Cash Flow"),
)


def insider_trade_record(
    *,
    ticker: str,
    issuer: str | None,
    name: str | None,
    filing_date: str | None,
    transaction_date: str | None,
    transaction_type: str | None,
    transaction_shares: int | float | None,
    transaction_price_per_share: int | float | None,
    transaction_value: int | float | None,
    shares_owned_after_transaction: int | float | None,
) -> JsonObject:
    record: JsonObject = {"ticker": ticker}
    _set(record, "issuer", issuer)
    _set(record, "name", name)
    _set(record, "filing_date", filing_date)
    _set(record, "transaction_date", transaction_date)
    _set(record, "transaction_type", transaction_type)
    _set(record, "transaction_shares", _clean_number(transaction_shares))
    _set(record, "transaction_price_per_share", _number(transaction_price_per_share))
    _set(record, "transaction_value", _clean_number(transaction_value))
    _set(
        record,
        "shares_owned_after_transaction",
        _clean_number(shares_owned_after_transaction),
    )
    return record


def screener_search_result(
    *,
    ticker: str,
    exchange: str | None,
    market_cap: str | None,
    last_sale: str | None,
    net_change: str | None,
    percent_change: str | None,
) -> JsonObject:
    record: JsonObject = {"ticker": ticker}
    _set(record, "exchange", exchange)
    _set(record, "market_cap", market_cap)
    _set(record, "last_sale", last_sale)
    _set(record, "net_change", net_change)
    _set(record, "percent_change", percent_change)
    return record


def screener_filters_response() -> JsonObject:
    """The executable filter catalog for the validated Nasdaq screener route."""
    return {
        "metrics": {
            "company": [
                {
                    "field": "exchange",
                    "operators": ["eq"],
                    "values": ["NASDAQ", "NYSE", "AMEX"],
                },
                {
                    "field": "market_cap",
                    "operators": ["eq"],
                    "values": ["mega", "large", "mid", "small", "micro", "nano"],
                },
            ]
        },
        "operators": ["eq"],
    }
