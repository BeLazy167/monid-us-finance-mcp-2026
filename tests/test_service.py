from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal
from typing import override

import pytest

from monid_finance_mcp.client import (
    MonidClientProtocol,
    MonidProviderHTTPError,
    MonidRun,
    MonidSchemaError,
    MonidTimeoutError,
)
from monid_finance_mcp.models import EnvelopeDict, JsonObject, JsonValue, Money
from monid_finance_mcp.service import FinanceService


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


def data_object(response: EnvelopeDict, key: str) -> JsonObject:
    value = response["data"][key]
    assert isinstance(value, dict)
    return value


def data_records(response: EnvelopeDict, key: str) -> list[JsonObject]:
    value = response["data"][key]
    assert isinstance(value, list)
    records: list[JsonObject] = []
    for item in value:
        assert isinstance(item, dict)
        records.append(item)
    return records


def line_item(record: JsonObject, name: str) -> JsonValue:
    values = record["line_items"]
    assert isinstance(values, list)
    for value in values:
        assert isinstance(value, dict)
        if value.get("name") == name:
            return value.get("value")
    raise AssertionError(f"Missing line item {name}")


CATALOG: JsonObject = {
    "data": [
        {
            "ticker": "AAPL",
            "companyName": "Apple Inc.",
            "country": "US",
            "exchange": "NASDAQ",
        }
    ]
}
SUMMARY: JsonObject = {
    "currentPrice": 230.1,
    "marketCap": 3_400_000_000_000,
    "peRatio": 34.2,
    "currency": "USD",
    "updatedAt": "2026-09-03T20:00:00Z",
}
STATEMENTS: JsonObject = {
    "data": {
        "incomeStatement": {
            "annual": [
                {"reportDate": "2023-12-31", "revenue": 80},
                {"reportDate": "2025-12-31", "revenue": 100},
                {"reportDate": "2024-12-31", "revenue": 90},
            ],
            "quarterly": [
                {"reportDate": "2026-03-31", "revenue": 25},
                {"reportDate": "2025-12-31", "revenue": 24},
                {"reportDate": "2025-09-30", "revenue": 23},
            ],
        },
        "balanceSheet": {
            "annual": [{"reportDate": "2025-12-31", "totalAssets": 400}],
            "quarterly": [{"reportDate": "2026-03-31", "totalAssets": 410}],
        },
        "cashFlow": {
            "annual": [{"reportDate": "2025-12-31", "freeCashFlow": 70}],
            "quarterly": [{"reportDate": "2026-03-31", "freeCashFlow": 17}],
        },
    }
}
FILINGS: JsonValue = [
    {
        "filingDate": "2026-02-01",
        "reportDate": "2025-12-31",
        "form": "10-K",
        "primaryDocumentUrl": "https://www.sec.gov/Archives/a.htm",
    },
    {
        "filingDate": "2025-11-01",
        "reportDate": "2025-09-30",
        "form": "10-Q",
        "primaryDocumentUrl": "https://www.sec.gov/Archives/b.htm",
    },
]
OHLCV: JsonValue = [
    [1767225600, 30, 34, 29, 33, 300],  # 2026-01-01
    [1767139200, 20, 24, 19, 23, 200],  # 2025-12-31
    [1767052800, 10, 14, 9, 13, 100],  # 2025-12-30
]
NEWS: JsonObject = {
    "data": [
        {"id": "old", "title": "Old", "published_at": "2026-01-01T12:00:00Z"},
        {"id": "new", "title": "New", "published_at": "2026-02-01T12:00:00Z"},
        {"id": "middle", "title": "Middle", "published_at": "2026-01-15T12:00:00Z"},
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


@pytest.mark.asyncio
async def test_good_data_for_all_published_tools() -> None:
    service = FinanceService(full_client())

    facts = await service.get_company_facts("aapl")
    income = await service.get_income_statement("AAPL", period="annual", limit=2)
    balance = await service.get_balance_sheet("AAPL", period="quarterly", limit=1)
    cash = await service.get_cash_flow_statement("AAPL", period="annual", limit=1)
    metrics = await service.get_financial_metrics_snapshot("AAPL")
    filings = await service.get_filings("AAPL", filing_type=["10-K"], limit=1)
    prices = await service.get_stock_prices(
        "AAPL", start_date="2025-12-30", end_date="2025-12-31", interval="day"
    )
    price = await service.get_stock_price("AAPL")
    news = await service.get_news("AAPL", limit=2)

    assert data_object(facts, "company_facts")["name"] == "Apple Inc."
    assert data_object(facts, "market_summary")["price"] == 230.1
    assert facts["total_cost"]["value"] == pytest.approx(0.0012)
    assert facts["total_cost"]["complete"]
    income_rows = data_records(income, "income_statements")
    assert [row["report_period"] for row in income_rows] == ["2025-12-31", "2024-12-31"]
    assert line_item(income_rows[0], "revenue") == 100
    assert line_item(data_records(balance, "balance_sheets")[0], "total_assets") == 410
    assert line_item(data_records(cash, "cash_flow_statements")[0], "free_cash_flow") == 70
    assert data_object(metrics, "financial_metrics_snapshot")["market_cap"] == 3_400_000_000_000
    assert [item["form"] for item in data_records(filings, "filings")] == ["10-K"]
    assert [row["date"] for row in data_records(prices, "prices")] == [
        "2025-12-30",
        "2025-12-31",
    ]
    assert data_object(price, "stock_price")["price"] == 230.1
    assert [item["id"] for item in data_records(news, "news")] == ["new", "middle"]
    responses = [facts, income, balance, cash, metrics, filings, prices, price, news]
    assert all(response["partial_errors"] == [] for response in responses)
    assert all("as_of" in item and "retrieved_at" in item for item in facts["provenance"])


@pytest.mark.asyncio
async def test_invalid_ticker_fails_before_paid_call() -> None:
    client = full_client()
    response = await FinanceService(client).get_stock_price("AAPL; rm -rf /")

    assert client.calls == []
    assert response["partial_errors"][0]["code"] == "invalid_input"
    assert response["total_cost"]["value"] == 0


@pytest.mark.asyncio
async def test_empty_data_is_not_silent() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/filings")] = completed("/equities/v1/filings", [])

    response = await FinanceService(client).get_filings("AAPL")

    assert response["data"]["filings"] == []
    assert response["warnings"] == ["DefiLlama returned no filings for AAPL."]


@pytest.mark.asyncio
async def test_provider_429_keeps_failed_run_provenance_and_cost() -> None:
    client = full_client()
    failed = completed("/equities/v1/summary", {"error": "rate limited"})
    failed = MonidRun(
        provider=failed.provider,
        endpoint=failed.endpoint,
        run_id=failed.run_id,
        status=failed.status,
        output=failed.output,
        provider_http_status=429,
        cost=failed.cost,
        created_at=failed.created_at,
        completed_at=failed.completed_at,
    )
    client.outcomes[("defillama", "/equities/v1/summary")] = MonidProviderHTTPError(failed)

    response = await FinanceService(client).get_stock_price("AAPL")

    assert response["data"]["stock_price"] is None
    assert response["partial_errors"][0]["code"] == "provider_http_error"
    assert response["provenance"][0]["provider_http_status"] == 429
    assert response["total_cost"]["value"] == pytest.approx(0.0006)


@pytest.mark.asyncio
async def test_timeout_is_a_typed_partial_error() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/ohlcv")] = MonidTimeoutError(
        "defillama", "/equities/v1/ohlcv", "run exceeded 3 seconds"
    )

    response = await FinanceService(client).get_stock_prices(
        "AAPL", start_date="2025-12-30", end_date="2025-12-31"
    )

    assert response["data"]["prices"] == []
    assert response["partial_errors"][0]["code"] == "timeout"
    assert not response["total_cost"]["complete"]


@pytest.mark.asyncio
async def test_provider_schema_drift_is_visible() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/statements")] = completed(
        "/equities/v1/statements", {"unexpected": []}
    )

    response = await FinanceService(client).get_income_statement("AAPL")

    assert response["data"]["income_statements"] == []
    assert response["partial_errors"][0]["code"] == "schema_drift"
    assert response["total_cost"]["value"] == pytest.approx(0.0006)


@pytest.mark.asyncio
async def test_period_limit_and_report_date_filters_are_deterministic() -> None:
    service = FinanceService(full_client())

    response = await service.get_income_statement(
        "AAPL",
        period="quarterly",
        limit=2,
        report_period_gte="2025-10-01",
        report_period_lte="2026-12-31",
    )

    assert [row["report_period"] for row in data_records(response, "income_statements")] == [
        "2026-03-31",
        "2025-12-31",
    ]


@pytest.mark.asyncio
async def test_bad_period_limit_and_dates_fail_before_paid_call() -> None:
    client = full_client()
    service = FinanceService(client)

    invalid_period = await service.get_income_statement("AAPL", period="monthly")
    invalid_limit = await service.get_filings("AAPL", limit=0)
    invalid_dates = await service.get_stock_prices(
        "AAPL", start_date="2026-01-02", end_date="2026-01-01"
    )

    assert client.calls == []
    assert invalid_period["partial_errors"][0]["code"] == "invalid_input"
    assert invalid_limit["partial_errors"][0]["code"] == "invalid_input"
    assert invalid_dates["partial_errors"][0]["code"] == "invalid_input"


@pytest.mark.asyncio
async def test_company_facts_returns_partial_data_when_summary_fails() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/summary")] = MonidSchemaError(
        "defillama", "/equities/v1/summary", "missing price payload"
    )

    response = await FinanceService(client).get_company_facts("AAPL")

    assert data_object(response, "company_facts")["name"] == "Apple Inc."
    assert response["data"]["market_summary"] is None
    assert response["partial_errors"][0]["code"] == "schema_drift"
    assert response["total_cost"]["value"] == pytest.approx(0.0006)


@pytest.mark.asyncio
async def test_live_defillama_statement_matrix_is_pivoted_without_losing_values() -> None:
    client = full_client()
    matrix: JsonObject = {
        "incomeStatement": {
            "labels": ["Revenue", "Net Income"],
            "children": [[], []],
            "annual": {
                "periodEnding": ["2025-09-27", "2024-09-28"],
                "values": [[100, 90], [25, 20]],
            },
            "quarterly": {
                "periodEnding": ["2025-09-27"],
                "values": [[30], [8]],
            },
        },
        "balanceSheet": {
            "labels": [],
            "children": [],
            "annual": {"periodEnding": [], "values": []},
        },
        "cashflow": {"labels": [], "children": [], "annual": {"periodEnding": [], "values": []}},
    }
    client.outcomes[("defillama", "/equities/v1/statements")] = completed(
        "/equities/v1/statements", matrix
    )

    response = await FinanceService(client).get_income_statement("AAPL", limit=1)

    rows = data_records(response, "income_statements")
    assert rows[0]["report_period"] == "2025-09-27"
    assert line_item(rows[0], "revenue") == 100
    assert line_item(rows[0], "net_income") == 25
    assert rows[0]["provider_fields"] == {"Revenue": 100, "Net Income": 25}


@pytest.mark.asyncio
async def test_not_found_summary_is_schema_drift_not_successful_price() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/summary")] = completed(
        "/equities/v1/summary", {"message": "not found"}
    )

    response = await FinanceService(client).get_stock_price("AAPL")

    assert response["data"]["stock_price"] is None
    assert response["partial_errors"][0]["code"] == "schema_drift"


@pytest.mark.asyncio
async def test_schema_error_after_paid_run_keeps_provenance_and_unknown_cost() -> None:
    client = full_client()
    run = completed("/equities/v1/summary", {"currentPrice": 230.1})
    missing_cost = MonidRun(
        provider=run.provider,
        endpoint=run.endpoint,
        run_id=run.run_id,
        status=run.status,
        output=run.output,
        provider_http_status=run.provider_http_status,
        cost=None,
        created_at=run.created_at,
        completed_at=run.completed_at,
        retrieved_at="2026-09-04T00:00:03Z",
    )
    client.outcomes[("defillama", "/equities/v1/summary")] = MonidSchemaError(
        "defillama",
        "/equities/v1/summary",
        "Monid run omitted measured billing cost.",
        run=missing_cost,
    )

    response = await FinanceService(client).get_stock_price("AAPL")

    assert response["provenance"][0]["run_id"] == run.run_id
    assert response["provenance"][0]["measured_cost"] is None
    assert response["total_cost"] == {"value": 0.0, "currency": "USD", "complete": False}
    assert response["partial_errors"][0]["code"] == "schema_drift"


@pytest.mark.asyncio
async def test_mixed_cost_currencies_are_not_added_as_usd() -> None:
    client = full_client()
    catalog = completed("/equities/v1/companies-list", CATALOG)
    client.outcomes[("defillama", "/equities/v1/companies-list")] = MonidRun(
        provider=catalog.provider,
        endpoint=catalog.endpoint,
        run_id=catalog.run_id,
        status=catalog.status,
        output=catalog.output,
        provider_http_status=catalog.provider_http_status,
        cost=Money(Decimal("0.0006"), "EUR"),
        created_at=catalog.created_at,
        completed_at=catalog.completed_at,
    )

    response = await FinanceService(client).get_company_facts("AAPL")

    assert response["total_cost"] == {"value": 0.0006, "currency": "EUR", "complete": False}
    assert any(error["code"] == "cost_currency_mismatch" for error in response["partial_errors"])


@pytest.mark.asyncio
async def test_timestamp_only_summary_is_not_metrics_data() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/summary")] = completed(
        "/equities/v1/summary", {"updatedAt": "2026-09-04T00:00:00Z"}
    )

    response = await FinanceService(client).get_financial_metrics_snapshot("AAPL")

    assert response["data"]["financial_metrics_snapshot"] is None
    assert response["partial_errors"][0]["code"] == "schema_drift"


@pytest.mark.asyncio
async def test_non_numeric_ohlcv_values_are_schema_drift() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/ohlcv")] = completed(
        "/equities/v1/ohlcv", [[1767139200, "bad", 24, 19, 23, 200]]
    )

    response = await FinanceService(client).get_stock_prices(
        "AAPL", start_date="2025-12-30", end_date="2025-12-31"
    )

    assert response["data"]["prices"] == []
    assert response["partial_errors"][0]["code"] == "schema_drift"


@pytest.mark.asyncio
@pytest.mark.parametrize("non_finite", [float("nan"), float("inf"), float("-inf")])
async def test_non_finite_summary_values_are_schema_drift(non_finite: float) -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/summary")] = completed(
        "/equities/v1/summary", {"currentPrice": non_finite}
    )

    response = await FinanceService(client).get_stock_price("AAPL")

    assert response["data"]["stock_price"] is None
    assert response["partial_errors"][0]["code"] == "schema_drift"


@pytest.mark.asyncio
async def test_non_finite_secondary_summary_field_is_schema_drift() -> None:
    client = full_client()
    client.outcomes[("defillama", "/equities/v1/summary")] = completed(
        "/equities/v1/summary", {"currentPrice": 230.1, "marketCap": float("inf")}
    )

    response = await FinanceService(client).get_financial_metrics_snapshot("AAPL")

    assert response["data"]["financial_metrics_snapshot"] is None
    assert response["partial_errors"][0]["code"] == "schema_drift"
