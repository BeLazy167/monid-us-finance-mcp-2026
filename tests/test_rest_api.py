from __future__ import annotations

import copy
from typing import Any, cast

import pytest
from fastapi.testclient import TestClient
from httpx import Response

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.rest_api import create_app
from monid_finance_mcp.service import FinanceService

from .test_service import STATEMENTS, FakeClient, completed, full_client

API_KEY = "test-api-key"


def client_for(fake: FakeClient, base_url: str = "http://localhost:8000") -> TestClient:
    service = FinanceService(fake)
    app = create_app(service, api_keys=(API_KEY,), base_url=base_url)
    # Use a localhost base URL so the MCP transport's Host allowlist accepts it.
    return TestClient(app, base_url=base_url)


AUTH = {"X-API-KEY": API_KEY}


def _get(client: TestClient, url: str, **kw: Any) -> Response:
    # pyright: ignore[reportUnknownMemberType]
    return cast(Response, client.get(url, **kw))  # pyright: ignore[reportUnknownMemberType]


def _post(client: TestClient, url: str, **kw: Any) -> Response:
    # pyright: ignore[reportUnknownMemberType]
    return cast(Response, client.post(url, **kw))  # pyright: ignore[reportUnknownMemberType]


def as_object(value: object) -> JsonObject:
    assert isinstance(value, dict)
    return cast(JsonObject, value)


def as_records(value: object) -> list[JsonObject]:
    assert isinstance(value, list)
    return [cast(JsonObject, item) for item in cast(list[object], value)]


def twelve_period_statements() -> JsonValue:
    """A copy of the shared statements fixture with 12 annual periods."""
    payload = copy.deepcopy(STATEMENTS)
    income = cast(JsonObject, payload["incomeStatement"])
    annual = cast(JsonObject, income["annual"])
    periods = [f"{year}-12-31" for year in range(2014, 2026)]
    annual["periodEnding"] = periods
    values = cast(list[object], annual["values"])
    rows = [
        [
            100 + year,
            120 + year,
            40 + year,
            30 + year,
            6 + year,
            24 + year,
            2.0,
            1.9,
            12.0,
            31 + year,
        ]
        for year in range(12)
    ]
    # Transpose so each label row has 12 columns (Revenue..EBIT order).
    income_labels = cast(list[object], income["labels"])
    for row_index, _label in enumerate(income_labels):
        values[row_index] = [row[row_index] for row in rows]
    annual_children = cast(JsonObject, annual["children"])
    for _key, child in annual_children.items():
        child_object = cast(JsonObject, child)
        child_values = cast(list[object], child_object["values"])
        # Non-Operating Interest Expense single row -> 12 columns.
        child_values[0] = list(range(1, 13))
    return payload


def auth_get(client: TestClient, url: str, **params: object) -> Response:
    return _get(client, url, headers=AUTH, params=params)


@pytest.fixture()
def paginated_client() -> TestClient:
    fake = full_client()
    fake.outcomes[("defillama", "/equities/v1/statements")] = completed(
        "/equities/v1/statements", twelve_period_statements()
    )
    return client_for(fake)


def test_auth_missing_key_401() -> None:
    client = client_for(full_client())
    response = _get(client, "/company/facts", params={"ticker": "AAPL"})
    assert response.status_code == 401
    body = as_object(response.json())
    assert body["error"] == "unauthorized"
    assert isinstance(body["message"], str)


def test_auth_wrong_key_401() -> None:
    from monid_finance_mcp.rest_api import create_app as build_app

    service = FinanceService(full_client())
    client = TestClient(build_app(service, api_keys=(API_KEY,)))
    response = _get(client, "/company/facts", headers={"X-API-KEY": "wrong"})
    assert response.status_code == 401
    assert as_object(response.json())["error"] == "unauthorized"


def test_any_nonempty_key_accepted_when_env_unset() -> None:
    import os

    os.environ.pop("API_KEYS", None)
    service = FinanceService(full_client())
    client = TestClient(create_app(service))
    response = _get(
        client, "/company/facts", headers={"X-API-KEY": "anything"}, params={"ticker": "AAPL"}
    )
    assert response.status_code == 200
    assert "company_facts" in as_object(response.json())


def test_income_statements_200_shape_and_next_page_url_rewrite(
    paginated_client: TestClient,
) -> None:
    response = auth_get(
        paginated_client,
        "/financials/income-statements",
        ticker="AAPL",
        period="annual",
        limit="12",
    )
    assert response.status_code == 200
    body = as_object(response.json())
    records = as_records(body["income_statements"])
    assert len(records) == 10  # one page of 10
    assert all(record["ticker"] == "AAPL" for record in records)
    next_url = body["next_page_url"]
    assert isinstance(next_url, str)
    assert next_url.startswith("http://localhost:8000")
    assert "api.monid-finance-mcp.example" not in next_url
    assert "cursor=" in next_url


def test_income_statements_as_reported_rejected() -> None:
    client = client_for(full_client())
    response = auth_get(
        client, "/financials/income-statements", ticker="AAPL", as_reported="true"
    )
    assert response.status_code == 400
    body = as_object(response.json())
    assert body["error"] == "bad_request"
    assert isinstance(body["message"], str)


def test_screener_post() -> None:
    fake = FakeClient(
        {
            ("nasdaq", "/get_stock_screener"): completed("/get_stock_screener", {
                "status": "success",
                "data": {
                    "data": {
                        "filters": {},
                        "table": {
                            "asOf": None,
                            "headers": {
                                "symbol": "Symbol",
                                "name": "Name",
                                "lastsale": "Last Sale",
                                "netchange": "Net Change",
                                "pctchange": "% Change",
                                "marketCap": "Market Cap",
                            },
                            "rows": [
                                {
                                    "symbol": "AAPL",
                                    "name": "Apple Inc.",
                                    "lastsale": "$328.21",
                                    "netchange": "-3.25",
                                    "pctchange": "-1.00%",
                                    "marketCap": "4,789,955,817,800",
                                    "url": "/market-activity/stocks/aapl",
                                }
                            ],
                        },
                        "totalrecords": 1,
                        "asof": "Last price as of Sep 3, 2026",
                    },
                    "message": None,
                    "status": {"rCode": 200},
                },
            })
        }
    )
    client = client_for(fake)
    response = _post(
        client,
        "/financials/search/screener",
        headers=AUTH,
        json={
            "filters": [{"field": "exchange", "operator": "eq", "value": "NASDAQ"}],
            "limit": 10,
        },
    )
    assert response.status_code == 200
    body = as_object(response.json())
    assert isinstance(body["search_results"], list)


def test_screener_filters_get() -> None:
    client = client_for(full_client())
    response = auth_get(client, "/financials/search/screener/filters")
    assert response.status_code == 200
    body = as_object(response.json())
    assert isinstance(body["metrics"], dict)
    assert isinstance(body["operators"], list)


def test_filings_shape() -> None:
    client = client_for(full_client())
    response = auth_get(client, "/filings", ticker="AAPL")
    assert response.status_code == 200
    body = as_object(response.json())
    assert isinstance(body["filings"], list)


def test_prices_interval_multiplier_rejected() -> None:
    client = client_for(full_client())
    response = auth_get(client, "/prices", ticker="AAPL", interval_multiplier="2")
    assert response.status_code == 400
    assert as_object(response.json())["error"] == "bad_request"


def test_prices_valid() -> None:
    client = client_for(full_client())
    response = auth_get(
        client,
        "/prices",
        ticker="AAPL",
        interval="day",
        start_date="2025-12-30",
        end_date="2026-01-01",
    )
    assert response.status_code == 200
    body = as_object(response.json())
    assert body["ticker"] == "AAPL"
    assert isinstance(body["prices"], list)


def test_not_implemented_route() -> None:
    client = client_for(full_client())
    response = auth_get(client, "/kpi/metrics", ticker="AAPL")
    assert response.status_code == 200
    body = as_object(response.json())
    assert body["error"] == "not_implemented"
    assert isinstance(body["message"], str)


def test_mcp_mount_initializes() -> None:
    with client_for(full_client()) as client:
        request = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "test", "version": "1.0"},
            },
        }
        response = _post(
            client,
            "/mcp",
            json=request,
            headers={"Accept": "application/json, text/event-stream"},
        )
        assert response.status_code == 200
        text = response.text
        # The MCP Streamable HTTP endpoint answers with SSE framing here.
        assert "jsonrpc" in text
        assert "2.0" in text
        assert "Monid Finance MCP" in text
