from __future__ import annotations

import base64
import json
from dataclasses import dataclass, field
from decimal import Decimal
from pathlib import Path
from typing import cast, override

import pytest

from monid_finance_mcp.client import (
    MonidClientProtocol,
    MonidProviderHTTPError,
    MonidRun,
    MonidTimeoutError,
)
from monid_finance_mcp.models import JsonObject, JsonValue, Money
from monid_finance_mcp.receipts import ReceiptsLedger, summarize_ledger
from monid_finance_mcp.service import FinanceService

FD_CONTRACT = json.loads(
    (Path(__file__).resolve().parents[1] / "docs" / "fd-contract-reference.json").read_text()
)


def completed(endpoint: str, output: JsonValue, *, run_id: str | None = None) -> MonidRun:
    return MonidRun(
        provider="context.dev" if endpoint == "/news/search" else "defillama",
        endpoint=endpoint,
        run_id=run_id or endpoint.rsplit("/", 1)[-1],
        status="COMPLETED",
        output=output,
        provider_http_status=200,
        cost=Money(value=Decimal("0.0009" if endpoint == "/news/search" else "0.0006")),
        created_at="2026-09-04T00:00:00Z",
        completed_at="2026-09-04T00:00:02Z",
    )


@dataclass
class FakeClient(MonidClientProtocol):
    outcomes: dict[tuple[str, str], MonidRun | Exception]
    calls: list[tuple[str, str, JsonObject | None, JsonObject | None]] = field(
        default_factory=lambda: list[tuple[str, str, JsonObject | None, JsonObject | None]]()
    )

    @override
    async def run(
        self,
        provider: str,
        endpoint: str,
        *,
        body: JsonObject | None = None,
        query_params: JsonObject | None = None,
        path_params: JsonObject | None = None,
    ) -> MonidRun:
        del path_params
        self.calls.append((provider, endpoint, body, query_params))
        outcome = self.outcomes[(provider, endpoint)]
        if isinstance(outcome, Exception):
            raise outcome
        return outcome


def ledger(tmp_path: Path) -> ReceiptsLedger:
    return ReceiptsLedger(tmp_path / "ledger.jsonl")


def service(client: FakeClient, tmp_path: Path) -> FinanceService:
    return FinanceService(client, ledger(tmp_path))


def cursor_offset(url_or_cursor: str) -> int:
    token = url_or_cursor
    if "cursor=" in token:
        token = token.split("cursor=", 1)[1]
    padded = token + "=" * (-len(token) % 4)
    loaded: object = json.loads(base64.urlsafe_b64decode(padded))
    assert isinstance(loaded, dict)
    payload = cast(dict[object, object], loaded)
    offset = payload.get("o")
    assert isinstance(offset, int) and not isinstance(offset, bool)
    return offset


CATALOG: JsonObject = {
    "data": [
        {
            "ticker": "AAPL",
            "companyName": "Apple Inc.",
            "country": "US",
        }
    ]
}
SUMMARY: JsonObject = {
    "currentPrice": 230.1,
    "marketCap": 3_400_000_000_000,
    "trailingPE": 34.2,
    "priceToBook": 55.0,
    "priceToRevenue": 8.9,
    "enterpriseValueToEbitda": 27.5,
    "priceChange1d": 1.2,
    "priceChangePercentage1d": 0.5,
    "revenueTTM": 400.0,
    "grossProfitTTM": 180.0,
    "earningsTTM": 100.0,
    "operatingProfitMarginTTM": 0.3,
    "updatedAt": "2026-09-03T20:00:00Z",
}

ANNUAL_DATES = ["2024-12-31", "2025-12-31"]
QUARTERLY_DATES = ["2025-06-30", "2025-09-30", "2025-12-31", "2026-03-31"]
STATEMENTS: JsonObject = {
    "incomeStatement": {
        "labels": ["Revenue", "Cost of Revenue", "Gross Profit", "Operating Income",
                   "Income Tax", "Net Income", "EPS (Basic)", "EPS (Diluted)",
                   "Shares Outstanding (Basic)", "EBIT"],
        "annual": {
            "periodEnding": ANNUAL_DATES,
            "values": [
                [100, 120],   # Revenue
                [60, 70],     # Cost of Revenue
                [40, 50],     # Gross Profit
                [30, 35],     # Operating Income
                [6, 7],       # Income Tax
                [24, 28],     # Net Income
                [2.0, 2.1],   # EPS (Basic)
                [1.9, 2.0],   # EPS (Diluted)
                [12.0, 13.0], # Shares Outstanding (Basic)
                [31, 36],     # EBIT
            ],
            "children": {
                "Non-Operating Items": {"values": [[3, 4]]},
            },
        },
        "quarterly": {
            "periodEnding": QUARTERLY_DATES,
            "values": [
                [20, 22, 24, 25],  # Revenue
                [12, 13, 14, 15],  # Cost of Revenue
                [8, 9, 10, 10],    # Gross Profit
                [6, 7, 8, 8],      # Operating Income
                [1, 1, 2, 2],      # Income Tax
                [5, 6, 6, 6],      # Net Income
                [0.5, 0.6, 0.5, 0.5],  # EPS (Basic)
                [0.5, 0.5, 0.5, 0.5],  # EPS (Diluted)
                [10.0, 10.0, 11.0, 12.0],  # Shares Outstanding (Basic)
                [6, 7, 8, 8],      # EBIT
            ],
            "children": {
                "Non-Operating Items": {"values": [[1, 1, 2, 2]]},
            },
        },
        "children": {
            "annual": {
                "Non-Operating Items": {"labels": ["Non-Operating Interest Expense"]}
            },
            "quarterly": {
                "Non-Operating Items": {"labels": ["Non-Operating Interest Expense"]}
            },
        },
    },
    "balanceSheet": {
        "labels": ["Total Assets", "Total Current Assets", "Total Liabilities",
                   "Total Shareholders Equity"],
        "annual": {
            "periodEnding": ANNUAL_DATES,
            "values": [
                [400, 420],  # Total Assets
                [200, 210],  # Total Current Assets
                [250, 260],  # Total Liabilities
                [150, 160],  # Total Shareholders Equity
            ],
        },
        "quarterly": {
            "periodEnding": QUARTERLY_DATES,
            "values": [
                [405, 410, 415, 420],
                [202, 206, 208, 210],
                [252, 255, 258, 260],
                [153, 155, 157, 160],
            ],
        },
        "children": {},
    },
    "cashflow": {
        "labels": ["Cash Flow from Operating Activities", "Free Cash Flow", "Net Cash Flow",
                   "End Cash Position", "Net Income"],
        "annual": {
            "periodEnding": ANNUAL_DATES,
            "values": [
                [60, 70],  # CFO
                [50, 60],  # FCF
                [10, 11],  # Net Cash Flow
                [30, 33],  # End Cash Position
                [24, 28],  # Net Income
            ],
        },
        "quarterly": {
            "periodEnding": QUARTERLY_DATES,
            "values": [
                [14, 16, 18, 19],
                [12, 13, 15, 16],
                [2, 3, 3, 4],
                [31, 32, 33, 33],
                [5, 6, 6, 6],
            ],
        },
        "children": {},
    },
}
FILINGS: JsonValue = [
    {
        "filingDate": "2026-02-01",
        "reportDate": "2025-12-31",
        "form": "10-K",
        "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20241231.htm",
    },
    {
        "filingDate": "2026-01-15",
        "reportDate": "2025-12-31",
        "form": "8-K",
        "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019326000001/a.htm",
    },
    {
        "filingDate": "2025-11-01",
        "reportDate": "2025-09-30",
        "form": "10-Q",
        "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250930.htm",
    },
]
OHLCV: JsonValue = [
    [1767225600, 30, 34, 29, 33, 300],  # 2026-01-01
    [1767139200, 20, 24, 19, 23, 200],  # 2025-12-31
    [1767052800, 10, 14, 9, 13, 100],  # 2025-12-30
]
NEWS: JsonObject = {
    "data": [
        {"id": "old", "title": "Old", "published_at": "2026-01-01T12:00:00Z",
         "url": "https://example.com/old", "source": "example.com"},
        {"id": "new", "title": "New", "published_at": "2026-02-01T12:00:00Z",
         "url": "https://example.com/new", "source": "example.com"},
    ],
    "has_more": False,
    "next_cursor": None,
}


def full_client() -> FakeClient:
    return FakeClient(
        {
            ("defillama", "/equities/v1/companies-list"): completed(
                "/equities/v1/companies-list", CATALOG
            ),
            ("defillama", "/equities/v1/summary"): completed("/equities/v1/summary", SUMMARY),
            ("defillama", "/equities/v1/statements"): completed(
                "/equities/v1/statements", STATEMENTS
            ),
            ("defillama", "/equities/v1/filings"): completed("/equities/v1/filings", FILINGS),
            ("defillama", "/equities/v1/ohlcv"): completed("/equities/v1/ohlcv", OHLCV),
            ("context.dev", "/news/search"): completed("/news/search", NEWS),
        }
    )


def fd_keys(record_name: str) -> set[str]:
    schema = FD_CONTRACT[record_name]
    return set(schema.keys())


def as_records(value: object) -> list[JsonObject]:
    assert isinstance(value, list)
    items = cast(list[object], value)
    records: list[JsonObject] = []
    for item in items:
        assert isinstance(item, dict)
        records.append(cast(JsonObject, item))
    return records


def as_object(value: object) -> JsonObject:
    assert isinstance(value, dict)
    return cast(JsonObject, value)


def in_schema_order(record: JsonObject, record_name: str) -> bool:
    schema_order = list(FD_CONTRACT[record_name].keys())
    positions = [schema_order.index(key) for key in record if key in schema_order]
    return positions == sorted(positions) and all(key in schema_order for key in record)


@pytest.mark.asyncio
async def test_company_facts_matches_fd_shape(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_company_facts(ticker="aapl")
    assert response == {"company_facts": {"ticker": "AAPL", "name": "Apple Inc."}}
    facts = as_object(response["company_facts"])
    assert set(facts) <= fd_keys("CompanyFacts")


@pytest.mark.asyncio
async def test_company_facts_unknown_ticker_is_not_found(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_company_facts(ticker="zzzz")
    assert response == {
        "error": "not_found",
        "message": "No US company record exists for ticker ZZZZ.",
    }


@pytest.mark.asyncio
async def test_company_facts_rejects_cik_and_bad_ticker(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    assert (await finance.get_company_facts(cik="0000320193"))["error"] == "bad_request"
    assert (await finance.get_company_facts(ticker="bad ticker!!"))["error"] == "bad_request"
    assert (await finance.get_company_facts())["error"] == "bad_request"


@pytest.mark.asyncio
async def test_income_statement_annual_matches_fd_contract(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_income_statement(ticker="AAPL", period="annual", limit=4)
    records = as_records(response["income_statements"])
    assert isinstance(records, list) and len(records) == 2
    newest = records[0]
    assert newest["report_period"] == "2025-12-31"
    assert newest["fiscal_period"] == "FY2025"
    assert newest["period"] == "annual"
    assert newest["ticker"] == "AAPL"
    assert newest["revenue"] == 120
    assert newest["cost_of_revenue"] == 70
    assert newest["gross_profit"] == 50
    assert newest["operating_income"] == 35
    assert newest["income_tax_expense"] == 7
    assert newest["net_income"] == 28
    assert newest["interest_expense"] == 4
    assert newest["ebit"] == 36
    assert newest["earnings_per_share"] == 2.1
    assert newest["earnings_per_share_diluted"] == 2.0
    assert newest["weighted_average_shares"] == 13.0
    assert set(newest) <= fd_keys("IncomeStatement")
    assert in_schema_order(newest, "IncomeStatement")
    assert "next_page_url" not in response


@pytest.mark.asyncio
async def test_income_statement_joins_filing_identity(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_income_statement(ticker="AAPL", period="annual")
    newest = as_records(response["income_statements"])[0]
    assert newest["accession_number"] == "0000320193-25-000079"
    assert newest["form_type"] == "10-K"
    assert newest["filing_date"] == "2026-02-01"
    filing_url = newest["filing_url"]
    assert isinstance(filing_url, str)
    assert filing_url.startswith("https://www.sec.gov/Archives/")


@pytest.mark.asyncio
async def test_income_statement_filing_date_filter(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_income_statement(
        ticker="AAPL", period="annual", filing_date_gte="2026-01-01"
    )
    records = as_records(response["income_statements"])
    assert isinstance(records, list)
    assert [record["report_period"] for record in records] == ["2025-12-31"]


@pytest.mark.asyncio
async def test_income_statement_quarterly_fiscal_labels(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_income_statement(ticker="AAPL", period="quarterly")
    records = as_records(response["income_statements"])
    assert isinstance(records, list) and len(records) == 4
    newest = records[0]
    assert newest["report_period"] == "2026-03-31"
    assert newest["fiscal_period"] == "Q1 FY2026"
    assert records[1]["fiscal_period"] == "Q4 FY2025"


@pytest.mark.asyncio
async def test_income_statement_ttm_derives_from_quarters(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_income_statement(ticker="AAPL", period="ttm")
    records = as_records(response["income_statements"])
    assert isinstance(records, list) and len(records) == 1
    ttm = records[0]
    assert ttm["report_period"] == "2026-03-31"
    assert ttm["period"] == "ttm"
    assert ttm["revenue"] == 91  # 20 + 22 + 24 + 25
    assert ttm["net_income"] == 23  # 5 + 6 + 6 + 6
    assert ttm["interest_expense"] == 6
    assert ttm["earnings_per_share"] == pytest.approx(2.1)  # 0.5 + 0.6 + 0.5 + 0.5
    assert ttm["weighted_average_shares"] == pytest.approx(10.75)  # mean of share counts
    assert "fiscal_period" not in ttm


@pytest.mark.asyncio
async def test_income_statement_report_period_filters(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_income_statement(
        ticker="AAPL", period="annual", report_period_gte="2025-01-01"
    )
    records = as_records(response["income_statements"])
    assert isinstance(records, list)
    assert [record["report_period"] for record in records] == ["2025-12-31"]
    exact = await finance.get_income_statement(
        ticker="AAPL", period="annual", report_period="2024-12-31"
    )
    records = as_records(exact["income_statements"])
    assert [as_object(record).get("revenue") for record in records] == [100]


@pytest.mark.asyncio
async def test_income_statement_cursor_pagination(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    first = await finance.get_income_statement(ticker="AAPL", period="quarterly", limit=4)
    token = first.get("next_page_url")
    assert token is None  # 4 records fit on one page of 10


@pytest.mark.asyncio
async def test_filings_pagination_walk(tmp_path: Path) -> None:
    many: list[JsonObject] = []
    for index in range(14):
        many.append(
            {
                "filingDate": f"2026-01-{index + 1:02d}",
                "reportDate": "2025-12-31",
                "form": "8-K",
                "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/x.htm",
            }
        )
    client = FakeClient(
        {("defillama", "/equities/v1/filings"): completed("/equities/v1/filings", many)}
    )
    finance = service(client, tmp_path)
    first = await finance.get_filings(ticker="AAPL", limit=14)
    records = first["filings"]
    assert isinstance(records, list) and len(records) == 10
    next_url = first.get("next_page_url")
    assert isinstance(next_url, str)
    token = next_url.split("cursor=", 1)[1]
    second = await finance.get_filings(ticker="AAPL", limit=14, cursor=token)
    records = second["filings"]
    assert isinstance(records, list) and len(records) == 4
    assert "next_page_url" not in second


@pytest.mark.asyncio
async def test_filings_filter_and_shape(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_filings(ticker="AAPL", filing_type=["10-K"])
    records = as_records(response["filings"])
    assert isinstance(records, list) and len(records) == 1
    record = records[0]
    assert record["filing_type"] == "10-K"
    assert record["report_date"] == "2025-12-31"
    assert record["filing_date"] == "2026-02-01"
    assert record["ticker"] == "AAPL"
    assert record["accession_number"] == "0000320193-25-000079"
    assert set(record) <= fd_keys("Filing")
    assert in_schema_order(record, "Filing")
    invalid = await finance.get_filings(ticker="AAPL", filing_type=["40-F"])
    assert invalid["error"] == "bad_request"


@pytest.mark.asyncio
async def test_balance_sheet_and_cash_flow_shapes(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    balance = await finance.get_balance_sheet(ticker="AAPL", period="annual")
    records = as_records(balance["balance_sheets"])
    assert len(records) == 2
    newest = records[0]
    assert newest["total_assets"] == 420
    assert newest["current_assets"] == 210
    assert newest["shareholders_equity"] == 160
    assert set(newest) <= fd_keys("BalanceSheet")
    assert in_schema_order(newest, "BalanceSheet")
    cash = await finance.get_cash_flow_statement(ticker="AAPL", period="annual")
    records = as_records(cash["cash_flow_statements"])
    assert len(records) == 2
    newest = records[0]
    assert newest["net_cash_flow_from_operations"] == 70
    assert newest["free_cash_flow"] == 60
    assert newest["ending_cash_balance"] == 33
    assert set(newest) <= fd_keys("CashFlowStatement")
    assert in_schema_order(newest, "CashFlowStatement")


@pytest.mark.asyncio
async def test_financial_metrics_snapshot_shape(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_financial_metrics_snapshot(ticker="AAPL")
    snapshot = as_object(response["snapshot"])
    assert isinstance(snapshot, dict)
    assert snapshot["ticker"] == "AAPL"
    assert snapshot["market_cap"] == 3_400_000_000_000
    assert snapshot["price_to_earnings_ratio"] == 34.2
    assert snapshot["gross_margin"] == pytest.approx(0.45)  # 180/400
    assert snapshot["net_margin"] == pytest.approx(0.25)  # 100/400
    assert set(snapshot) <= fd_keys("FinancialMetricSnapshot")


@pytest.mark.asyncio
async def test_stock_prices_day_and_month_aggregation(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    daily = await finance.get_stock_prices(
        ticker="AAPL", interval="day", start_date="2025-12-30", end_date="2026-01-01"
    )
    assert daily["ticker"] == "AAPL"
    records = as_records(daily["prices"])
    assert isinstance(records, list) and len(records) == 3
    assert [record["time"] for record in records] == [
        "2025-12-30",
        "2025-12-31",
        "2026-01-01",
    ]
    assert records[2]["open"] == 30 and records[2]["close"] == 33
    assert set(records[0]) <= fd_keys("Price")
    monthly = await finance.get_stock_prices(
        ticker="AAPL", interval="month", start_date="2025-12-30", end_date="2026-01-01"
    )
    records = as_records(monthly["prices"])
    assert len(records) == 2
    december = records[0]
    assert december["time"] == "2025-12-31"
    assert december["open"] == 10 and december["close"] == 23
    assert december["volume"] == 300  # 100 + 200


@pytest.mark.asyncio
async def test_stock_price_snapshot(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_stock_price("AAPL")
    snapshot = as_object(response["snapshot"])
    assert isinstance(snapshot, dict)
    assert snapshot["price"] == 230.1
    assert snapshot["ticker"] == "AAPL"
    assert snapshot["day_change"] == 1.2
    assert snapshot["day_change_percent"] == 0.5
    assert snapshot["time"] == "2026-09-03T20:00:00Z"
    assert set(snapshot) <= fd_keys("PriceSnapshot")


@pytest.mark.asyncio
async def test_news_shape(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_news(ticker="AAPL", limit=5)
    records = as_records(response["news"])
    assert isinstance(records, list) and len(records) == 2
    newest = records[0]
    assert newest == {
        "ticker": "AAPL",
        "title": "New",
        "source": "example.com",
        "date": "2026-02-01T12:00:00Z",
        "url": "https://example.com/new",
    }
    assert set(newest) <= fd_keys("News")
    missing = await finance.get_news()
    assert missing["error"] == "bad_request"


@pytest.mark.asyncio
async def test_upstream_failures_map_to_fd_errors_and_ledger(tmp_path: Path) -> None:
    run = completed("/equities/v1/summary", SUMMARY)
    client = FakeClient(
        {
            ("defillama", "/equities/v1/summary"): MonidProviderHTTPError(
                replace_run_with_status(run, 429)
            ),
        }
    )
    finance = service(client, tmp_path)
    response = await finance.get_stock_price("AAPL")
    assert response["error"] == "upstream_error"
    assert "429" in str(response["message"])
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 1
    assert summary["failures"] == 1


@pytest.mark.asyncio
async def test_timeout_maps_to_fd_error(tmp_path: Path) -> None:
    run = completed("/equities/v1/summary", SUMMARY)
    client = FakeClient(
        {
            ("defillama", "/equities/v1/summary"): MonidTimeoutError(
                "defillama", "/equities/v1/summary", "timed out"
            ),
        }
    )
    del run
    finance = service(client, tmp_path)
    response = await finance.get_stock_price("AAPL")
    assert response["error"] == "timeout"


@pytest.mark.asyncio
async def test_receipts_recorded_per_call(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    await finance.get_stock_price("AAPL")
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 1
    assert summary["failures"] == 0
    tools = summary["tools"]
    assert isinstance(tools, dict)
    entry = tools["get_stock_price"]
    assert isinstance(entry, dict)
    assert entry["calls"] == 1


@pytest.mark.asyncio
async def test_invalid_cursor_rejected(tmp_path: Path) -> None:
    finance = service(full_client(), tmp_path)
    response = await finance.get_filings(ticker="AAPL", cursor="not-a-cursor")
    assert response["error"] == "invalid_cursor"


def replace_run_with_status(run: MonidRun, status: int) -> MonidRun:
    from dataclasses import replace

    return replace(run, provider_http_status=status, status="PROVIDER_ERROR")
