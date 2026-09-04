from __future__ import annotations

import json
from dataclasses import dataclass, field
from decimal import Decimal
from pathlib import Path
from typing import cast, override

import pytest

from monid_finance_mcp.client import (
    MonidClientProtocol,
    MonidRun,
)
from monid_finance_mcp.models import JsonObject, JsonValue, Money
from monid_finance_mcp.receipts import ReceiptsLedger, summarize_ledger
from monid_finance_mcp.service import FinanceService

FD_CONTRACT = json.loads(
    (Path(__file__).resolve().parents[1] / "docs" / "fd-contract-reference.json").read_text()
)


def completed(provider: str, endpoint: str, output: JsonValue, cost: str) -> MonidRun:
    return MonidRun(
        provider=provider,
        endpoint=endpoint,
        run_id=f"{provider}-{endpoint}".replace("/", "-"),
        status="COMPLETED",
        output=output,
        provider_http_status=200,
        cost=Money(value=Decimal(cost)),
        created_at="2026-09-04T00:00:00Z",
        completed_at="2026-09-04T00:00:02Z",
    )


@dataclass
class FakeClient(MonidClientProtocol):
    """Test client that routes Context.dev calls by request URL."""

    outcomes: dict[tuple[str, str], MonidRun | Exception]
    url_routes: dict[tuple[str, str, str], MonidRun | Exception] = field(
        default_factory=lambda: dict[tuple[str, str, str], MonidRun | Exception]()
    )
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
        routed = self._route(provider, endpoint, body, query_params)
        outcome = routed if routed is not None else self.outcomes[(provider, endpoint)]
        if isinstance(outcome, Exception):
            raise outcome
        return outcome

    def _route(
        self,
        provider: str,
        endpoint: str,
        body: JsonObject | None,
        query_params: JsonObject | None,
    ) -> MonidRun | Exception | None:
        url: object = None
        if query_params is not None:
            url = query_params.get("url")
        if url is None and body is not None:
            url = body.get("url")
        if not isinstance(url, str):
            return None
        return self.url_routes.get((provider, endpoint, url))


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


def make_service(client: FakeClient, tmp_path: Path) -> FinanceService:
    return FinanceService(client, ReceiptsLedger(tmp_path / "ledger.jsonl"))


def fd_keys(record_name: str) -> set[str]:
    schema = FD_CONTRACT[record_name]
    return set(schema.keys())


def in_schema_order(record: JsonObject, record_name: str) -> bool:
    schema_order = list(FD_CONTRACT[record_name].keys())
    positions = [schema_order.index(key) for key in record if key in schema_order]
    return positions == sorted(positions) and all(key in schema_order for key in record)


# ---------------------------------------------------------------------------
# Shared fixtures
# ---------------------------------------------------------------------------

TEN_K_URL = (
    "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20250927.htm"
)
TEN_Q_URL = (
    "https://www.sec.gov/Archives/edgar/data/320193/000032019326000010/aapl-20251231.htm"
)

FILINGS: list[JsonObject] = [
    {
        "filingDate": "2026-01-10",
        "reportDate": "2025-09-27",
        "form": "10-K",
        "primaryDocumentUrl": TEN_K_URL,
    },
    {
        "filingDate": "2026-01-20",
        "reportDate": "2025-12-31",
        "form": "10-Q",
        "primaryDocumentUrl": TEN_Q_URL,
    },
]


def extract_envelope(url: str, data: JsonObject) -> JsonObject:
    return {"status": "ok", "url": url, "urls_analyzed": 1, "data": data}


def scrape_envelope(url: str, markdown: str) -> JsonObject:
    return {
        "success": True,
        "url": url,
        "markdown": markdown,
        "contentLength": len(markdown),
    }


SEGMENT_DATA: JsonObject = {
    "product_net_sales": [
        {
            "name": "iPhone",
            "metric": "net sales",
            "unit": "USD",
            "values": [
                {"fiscal_year": 2025, "period_end": "2025-09-27", "value": 69000000000}
            ],
            "evidence_quote": "iPhone net sales were $69.0 billion.",
            "evidence_section": "Segment note",
        },
        {
            "name": "Services",
            "metric": "net sales",
            "unit": "USD",
            "values": [
                {"fiscal_year": 2025, "period_end": "2025-09-27", "value": 30000000000}
            ],
            "evidence_quote": "Services net sales were $30.0 billion.",
            "evidence_section": "Segment note",
        },
    ],
    "geographic_reportable_segment_net_sales": [
        {
            "name": "Americas",
            "metric": "net sales",
            "unit": "USD",
            "values": [
                {"fiscal_year": 2025, "period_end": "2025-09-27", "value": 58500000000}
            ],
            "evidence_quote": "Americas net sales were $58.5 billion.",
            "evidence_section": "Segment note",
        }
    ],
}

KPI_DATA: JsonObject = {
    "kpis": [
        {
            "name": "load_factor",
            "unit": "%",
            "period": "Q1 2026",
            "value_text": "82.0%",
            "value": 82.0,
            "basis": "quarterly",
            "evidence_quote": "Passenger load factor was 82.0 percent.",
        },
        {
            "name": "same_store_sales",
            "unit": "%",
            "period": "FY 2025",
            "value_text": "3.5%",
            "value": 3.5,
            "basis": "annual",
            "evidence_quote": "Comparable store sales increased 3.5 percent.",
        },
        {
            "name": "revenue",
            "unit": "USD",
            "period": "FY 2026",
            "value_text": "10-12B",
            "value": None,
            "basis": "annual",
            "evidence_quote": "We guide revenue of $10 to $12 billion.",
        },
    ]
}

INSTITUTIONAL: JsonObject = {
    "status": "success",
    "data": {
        "ticker": "AAPL",
        "company": "Apple Inc.",
        "results": [
            {
                "name": "Vanguard Group Inc",
                "shares": 1234567,
                "value": 345678901,
                "report_period": "2025-12-31",
            },
            {
                "name": "BlackRock Inc",
                "shares": 987654,
                "value": 276543210,
                "report_period": "2025-09-30",
            },
        ],
    },
}

FUND_PAGE_URL = (
    "https://www.ssga.com/us/en/intermediary/etfs/funds/spdr-sp-500-etf-trust-spy"
)
FUND_XLSX_URL = (
    "https://www.ssga.com/us/en/intermediary/etfs/funds/spy/holdings.xlsx"
)
FUND_MARKDOWN = """SPDR S&P 500 ETF Trust holdings
| Ticker | Name                | CUSIP     | Weight (%) | Market Value | Shares   |
|--------|---------------------|-----------|-----------|--------------|----------|
| AAPL   | Apple Inc.          | 037833100 | 6.71%     | $1,234,567,890 | 1234567 |
| MSFT   | Microsoft Corp      | 594918104 | 6.50%     | $1,200,000,000 | 1000000 |
|        | US Treasury Note 4% | 9128283M5 | 1.20%     | $100,000,000   | 100000   |
As of September 30, 2025
"""

FUND_SEARCH: JsonObject = {
    "results": [
        {
            "url": FUND_PAGE_URL,
            "title": "SPDR S&P 500 ETF Trust",
        },
        {
            "url": FUND_XLSX_URL,
            "title": "SPY holdings xlsx",
        },
    ]
}

BANK_MARKDOWN: dict[str, str] = {
    "FED": (
        "The Federal Open Market Committee decided at its meeting on "
        "January 29, 2026 to lower the target range for the federal funds "
        "rate to 4.25 to 4.50 percent."
    ),
    "ECB": (
        "Effective 12 March 2025 the interest rate on the main refinancing "
        "operations is 2.15%."
    ),
    "BOE": (
        "The Monetary Policy Committee sets the Bank Rate at 4.75%. "
        "The decision was announced on 7 August 2025."
    ),
    "BOJ": (
        "The Bank decided to maintain the short-term policy interest rate "
        "at around 0.5 percent, announced January 24, 2025."
    ),
}

CALENDAR: JsonObject = {
    "data": {
        "rows": [
            {"symbol": "AAPL", "reportDate": "2026-01-20"},
            {"symbol": "MSFT", "reportDate": "2026-01-21"},
        ]
    }
}


def bank_urls() -> dict[str, str]:
    from monid_finance_mcp.providers.us.interest_rates import BANK_SPECS

    return {spec.bank: spec.url for spec in BANK_SPECS}


def statement_fixture() -> JsonObject:
    from tests.test_gate_b import statement_fixture as gate_b_statements

    return gate_b_statements()


def build_client(
    *,
    extract: dict[str, JsonObject] | None = None,
    scrape: dict[str, str] | None = None,
    filings: list[JsonObject] = FILINGS,
    extra: dict[tuple[str, str], MonidRun | Exception] | None = None,
) -> FakeClient:
    routes: dict[tuple[str, str, str], MonidRun | Exception] = {}
    if extract is not None:
        for url, data in extract.items():
            routes[("context.dev", "/web/extract", url)] = completed(
                "context.dev", "/web/extract", extract_envelope(url, data), "0.003"
            )
    if scrape is not None:
        for url, markdown in scrape.items():
            routes[("context.dev", "/web/scrape/markdown", url)] = completed(
                "context.dev", "/web/scrape/markdown", scrape_envelope(url, markdown), "0.0009"
            )
    outcomes: dict[tuple[str, str], MonidRun | Exception] = {
        ("defillama", "/equities/v1/filings"): completed(
            "defillama", "/equities/v1/filings", filings, "0.0006"
        )
    }
    if extra is not None:
        outcomes.update(extra)
    return FakeClient(outcomes, url_routes=routes)


# ---------------------------------------------------------------------------
# Segmented financials
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_segmented_financials_shape_and_key_order(tmp_path: Path) -> None:
    client = build_client(extract={TEN_K_URL: SEGMENT_DATA})
    finance = make_service(client, tmp_path)

    response = await finance.get_segmented_financials(ticker="aapl", period="annual")

    records = as_records(response["segmented_financials"])
    assert len(records) == 1
    record = records[0]
    assert record["ticker"] == "AAPL"
    assert record["report_period"] == "2025-09-27"
    assert record["fiscal_period"] == "FY2025"
    assert record["period"] == "annual"
    assert record["accession_number"] == "0000320193-25-000079"
    assert record["filing_url"] == TEN_K_URL
    income_statement = as_object(record["income_statement"])
    revenue = as_object(income_statement["revenue"])
    product = as_records(revenue["product"])
    assert [entry["label"] for entry in product] == ["iPhone", "Services"]
    assert product[0]["value"] == 69000000000.0
    segment = as_records(revenue["segment"])
    assert segment[0] == {"label": "Americas", "value": 58500000000.0}
    # metadata fields follow SegmentMetadata property order
    metadata = list(FD_CONTRACT["SegmentMetadata"])
    positions = [metadata.index(key) for key in record if key in metadata]
    assert positions == sorted(positions)
    assert set(record) <= set(FD_CONTRACT["SegmentMetadata"]) | {"income_statement"}
    assert set(response) <= set(FD_CONTRACT["SegmentedFinancialsResponse"])
    # the extract body carries the fact-checked single-page contract
    extract_call = client.calls[1]
    body = extract_call[2]
    assert body is not None
    assert body["url"] == TEN_K_URL
    assert body["factCheck"] is True
    assert body["maxDepth"] == 0


@pytest.mark.asyncio
async def test_get_segmented_financials_period_and_report_period_filters(
    tmp_path: Path,
) -> None:
    client = build_client(extract={TEN_K_URL: SEGMENT_DATA})
    finance = make_service(client, tmp_path)

    quarterly = await finance.get_segmented_financials(ticker="AAPL", period="quarterly")
    assert quarterly["error"] == "bad_request"

    ranged = await finance.get_segmented_financials(
        ticker="AAPL", period="annual", report_period_gte="2026-01-01"
    )
    assert ranged == {"segmented_financials": []}

    kept = await finance.get_segmented_financials(
        ticker="AAPL", period="annual", report_period_gte="2025-01-01"
    )
    assert len(as_records(kept["segmented_financials"])) == 1


@pytest.mark.asyncio
async def test_get_segmented_financials_not_found_without_10k(tmp_path: Path) -> None:
    client = build_client(filings=[FILINGS[1]], extract={TEN_K_URL: SEGMENT_DATA})
    finance = make_service(client, tmp_path)

    response = await finance.get_segmented_financials(ticker="AAPL")

    assert response["error"] == "not_found"
    ledger = summarize_ledger(tmp_path / "ledger.jsonl")
    assert ledger["calls"] == 1  # filings call only, no extract call


# ---------------------------------------------------------------------------
# KPI metrics / guidance / non-GAAP
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_kpi_metrics_shape_and_filters(tmp_path: Path) -> None:
    client = build_client(extract={TEN_Q_URL: KPI_DATA})
    finance = make_service(client, tmp_path)

    response = await finance.get_kpi_metrics(ticker="AAPL", period="quarterly")

    records = as_records(response["kpi_metrics"])
    assert len(records) == 1
    record = records[0]
    assert record == {
        "ticker": "AAPL",
        "metric_name": "load_factor",
        "value": 82.0,
        "unit": "%",
        "period": "Q1 2026",
        "period_type": "quarterly",
        "source_text": "Passenger load factor was 82.0 percent.",
        "source_url": TEN_Q_URL,
    }
    assert set(record) <= fd_keys("KPIMetric")
    assert in_schema_order(record, "KPIMetric")
    assert set(response) <= set(FD_CONTRACT["KPIMetricsResponse"])
    extract_call = client.calls[1]
    body = extract_call[2]
    assert body is not None
    assert body["url"] == TEN_Q_URL
    assert body["factCheck"] is True
    assert body["maxDepth"] == 0

    filtered = await finance.get_kpi_metrics(
        ticker="AAPL", period="quarterly", metric_name="SAME_STORE_SALES"
    )
    assert as_records(filtered["kpi_metrics"]) == []

    bad = await finance.get_kpi_metrics(ticker="AAPL", period="ttm")
    assert bad["error"] == "bad_request"
    too_many = await finance.get_kpi_metrics(ticker="AAPL", limit=51)
    assert too_many["error"] == "bad_request"


@pytest.mark.asyncio
async def test_get_kpi_guidance_shape_omits_unsourced_value(tmp_path: Path) -> None:
    client = build_client(extract={TEN_K_URL: KPI_DATA})
    finance = make_service(client, tmp_path)

    response = await finance.get_kpi_guidance(ticker="AAPL", period="annual")

    records = as_records(response["kpi_guidance"])
    assert len(records) == 2
    revenue = next(record for record in records if record["metric_name"] == "revenue")
    assert "value" not in revenue  # non-numeric guidance text is unsourced
    assert revenue["raw_text"] == "10-12B"
    assert revenue["source_text"] == "We guide revenue of $10 to $12 billion."
    assert revenue["source_url"] == TEN_K_URL
    assert set(revenue) <= fd_keys("KPIGuidanceItem")
    assert in_schema_order(revenue, "KPIGuidanceItem")


@pytest.mark.asyncio
async def test_get_kpi_non_gaap_shape(tmp_path: Path) -> None:
    client = build_client(extract={TEN_Q_URL: KPI_DATA})
    finance = make_service(client, tmp_path)

    response = await finance.get_kpi_non_gaap(ticker="AAPL", period="quarterly")

    records = as_records(response["kpi_non_gaap"])
    assert len(records) == 1
    assert records[0]["metric_name"] == "load_factor"
    assert set(records[0]) <= fd_keys("KPINonGAAPMetric")
    assert in_schema_order(records[0], "KPINonGAAPMetric")
    assert set(response) <= set(FD_CONTRACT["KPINonGAAPResponse"])


@pytest.mark.asyncio
async def test_get_kpi_report_period_range_empty_and_not_found(tmp_path: Path) -> None:
    client = build_client(extract={TEN_Q_URL: KPI_DATA})
    finance = make_service(client, tmp_path)

    out_of_range = await finance.get_kpi_metrics(
        ticker="AAPL", report_period_gte="2027-01-01"
    )
    assert out_of_range == {"kpi_metrics": []}

    missing = build_client(filings=[], extract={TEN_Q_URL: KPI_DATA})
    finance_missing = make_service(missing, tmp_path)
    not_found = await finance_missing.get_kpi_metrics(ticker="AAPL")
    assert not_found["error"] == "not_found"


# ---------------------------------------------------------------------------
# Interest rates
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_interest_rates_all_banks(tmp_path: Path) -> None:
    scrape = {url: BANK_MARKDOWN[bank] for bank, url in bank_urls().items()}
    client = build_client(scrape=scrape)
    finance = make_service(client, tmp_path)

    response = await finance.get_interest_rates()

    assert set(response) <= set(FD_CONTRACT["InterestRatesResponse"])
    records = as_records(response["interest_rates"])
    assert records == [
        {"bank": "FED", "name": "Federal Reserve", "rate": 4.375, "date": "2026-01-29"},
        {"bank": "ECB", "name": "European Central Bank", "rate": 2.15, "date": "2025-03-12"},
        {"bank": "BOE", "name": "Bank of England", "rate": 4.75, "date": "2025-08-07"},
        {"bank": "BOJ", "name": "Bank of Japan", "rate": 0.5, "date": "2025-01-24"},
    ]
    for record in records:
        assert in_schema_order(record, "InterestRate")
    assert len(client.calls) == 4
    ledger = summarize_ledger(tmp_path / "ledger.jsonl")
    assert ledger["calls"] == 4
    assert ledger["failures"] == 0


@pytest.mark.asyncio
async def test_get_interest_rates_omits_unparseable_bank(tmp_path: Path) -> None:
    markdown = dict(BANK_MARKDOWN)
    markdown["ECB"] = "The page is a chart with no readable rate text."
    scrape = {url: markdown[bank] for bank, url in bank_urls().items()}
    client = build_client(scrape=scrape)
    finance = make_service(client, tmp_path)

    response = await finance.get_interest_rates()

    banks = [record["bank"] for record in as_records(response["interest_rates"])]
    assert banks == ["FED", "BOE", "BOJ"]
    assert "ECB" not in banks


@pytest.mark.asyncio
async def test_get_interest_rates_all_fail_returns_empty(tmp_path: Path) -> None:
    client = build_client(scrape={url: "unparseable page" for url in bank_urls().values()})
    finance = make_service(client, tmp_path)

    response = await finance.get_interest_rates()

    assert response == {"interest_rates": []}
    ledger = summarize_ledger(tmp_path / "ledger.jsonl")
    assert ledger["calls"] == 4


# ---------------------------------------------------------------------------
# Index fund
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_index_fund_shape_sorted_and_paginated(tmp_path: Path) -> None:
    client = build_client(
        scrape={FUND_PAGE_URL: FUND_MARKDOWN},
        extra={
            ("context.dev", "/web/search"): completed(
                "context.dev", "/web/search", FUND_SEARCH, "0.0009"
            )
        },
    )
    finance = make_service(client, tmp_path)

    response = await finance.get_index_fund(ticker="SPY")

    assert set(response) <= set(FD_CONTRACT["IndexFundHoldingsResponse"])
    assert response["ticker"] == "SPY"
    fund = as_object(response["fund"])
    assert fund["name"] == "SPDR S&P 500 ETF Trust"
    assert fund["as_of"] == "2025-09-30"
    assert fund["total_holdings"] == 3
    assert fund["returned"] == 3
    assert fund["offset"] == 0
    holdings = as_records(response["holdings"])
    assert [entry.get("ticker") for entry in holdings] == ["AAPL", "MSFT", None]
    assert holdings[0]["weight"] == 6.71
    assert holdings[0]["shares"] == 1234567
    assert holdings[0]["market_value"] == 1234567890
    assert holdings[0]["asset_class"] == "equity"
    assert holdings[2]["asset_class"] == "bond"
    assert holdings[2]["name"] == "US Treasury Note 4%"
    for record in holdings:
        assert set(record) <= fd_keys("FundHolding")
        assert in_schema_order(record, "FundHolding")

    paged = await finance.get_index_fund(ticker="SPY", limit=2, offset=1)
    assert [entry.get("ticker") for entry in as_records(paged["holdings"])] == [
        "MSFT",
        None,
    ]


@pytest.mark.asyncio
async def test_get_index_fund_asset_class_filter_and_as_of(tmp_path: Path) -> None:
    client = build_client(
        scrape={FUND_PAGE_URL: FUND_MARKDOWN},
        extra={
            ("context.dev", "/web/search"): completed(
                "context.dev", "/web/search", FUND_SEARCH, "0.0009"
            )
        },
    )
    finance = make_service(client, tmp_path)

    bonds = await finance.get_index_fund(ticker="SPY", asset_class="bond")
    assert len(as_records(bonds["holdings"])) == 1
    assert as_records(bonds["holdings"])[0]["asset_class"] == "bond"

    stale = await finance.get_index_fund(ticker="SPY", as_of="2025-01-01")
    assert stale["error"] == "not_found"

    fresh = await finance.get_index_fund(ticker="SPY", as_of="2026-10-01")
    assert len(as_records(fresh["holdings"])) == 3

    bad_class = await finance.get_index_fund(ticker="SPY", asset_class="other")
    assert bad_class["error"] == "bad_request"


@pytest.mark.asyncio
async def test_get_index_fund_not_routable(tmp_path: Path) -> None:
    client = build_client(
        scrape={FUND_XLSX_URL: "This is a spreadsheet binary.", FUND_PAGE_URL: FUND_MARKDOWN},
        extra={
            ("context.dev", "/web/search"): completed(
                "context.dev", "/web/search",
                {"results": [{"url": FUND_XLSX_URL, "title": "holdings xlsx"}]},
                "0.0009",
            )
        },
    )
    finance = make_service(client, tmp_path)

    response = await finance.get_index_fund(ticker="SPY")

    assert response == {
        "error": "bad_request",
        "message": "holdings document not routable for SPY",
    }

    noresults = build_client(
        extra={
            ("context.dev", "/web/search"): completed(
                "context.dev", "/web/search", {"results": []}, "0.0009"
            )
        }
    )
    missing = await make_service(noresults, tmp_path).get_index_fund(ticker="SPY")
    assert missing["error"] == "bad_request"


# ---------------------------------------------------------------------------
# Institutional holdings
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_institutional_holdings_shape_and_order(tmp_path: Path) -> None:
    client = build_client(
        extra={
            ("secform4", "/get_institution_holders"): completed(
                "secform4", "/get_institution_holders", INSTITUTIONAL, "0.01"
            )
        }
    )
    finance = make_service(client, tmp_path)

    response = await finance.get_institutional_holdings(ticker="AAPL", limit=10)

    records = as_records(response["institutional_holdings"])
    assert len(records) == 2
    assert records[0]["filer_name"] == "Vanguard Group Inc"  # value desc
    highest = records[0]
    assert highest["ticker"] == "AAPL"
    assert highest["name_of_issuer"] == "Apple Inc."
    assert highest["shares"] == 1234567
    assert highest["value_usd"] == 345678901
    assert highest["report_period"] == "2025-12-31"
    assert set(highest) <= fd_keys("InstitutionalHolding")
    assert in_schema_order(highest, "InstitutionalHolding")
    assert set(response) <= set(FD_CONTRACT["InstitutionalHoldingsResponse"])
    assert response["ticker"] == "AAPL"

    ranged = await finance.get_institutional_holdings(
        ticker="AAPL", report_period_gte="2025-10-01"
    )
    assert [record["filer_name"] for record in as_records(ranged["institutional_holdings"])] == [
        "Vanguard Group Inc"
    ]


@pytest.mark.asyncio
async def test_get_institutional_holdings_rejects_filer_cik(tmp_path: Path) -> None:
    client = build_client()
    finance = make_service(client, tmp_path)

    response = await finance.get_institutional_holdings(
        filer_cik="0001067983", ticker="AAPL"
    )

    assert response == {
        "error": "bad_request",
        "message": "filer_cik lookup is not routed; pass ticker instead",
    }
    assert client.calls == []
    none = await finance.get_institutional_holdings()
    assert none["error"] == "bad_request"


# ---------------------------------------------------------------------------
# Earnings feed / news
# ---------------------------------------------------------------------------


@pytest.mark.asyncio
async def test_get_earnings_feed_composes_from_calendar(tmp_path: Path) -> None:
    client = build_client(extra={
        ("nasdaq", "/get_earnings_calendar"): completed(
            "nasdaq", "/get_earnings_calendar", CALENDAR, "0.01"
        ),
        ("defillama", "/equities/v1/statements"): completed(
            "defillama", "/equities/v1/statements", statement_fixture(), "0.0006"
        ),
        ("defillama", "/equities/v1/filings"): completed(
            "defillama", "/equities/v1/filings", FILINGS, "0.0006"
        ),
    })
    finance = make_service(client, tmp_path)

    response = await finance.get_earnings(limit=2)

    records = as_records(response["earnings"])
    assert {str(record.get("ticker") or "") for record in records} == {"AAPL", "MSFT"}
    required = {
        key
        for key, spec in FD_CONTRACT["EarningsRecord"].items()
        if spec.get("required")
    }
    for record in records:
        assert required <= set(record)
        assert in_schema_order(record, "EarningsRecord")


@pytest.mark.asyncio
async def test_get_earnings_feed_unparseable_calendar_is_honest_error(
    tmp_path: Path,
) -> None:
    client = build_client(extra={
        ("nasdaq", "/get_earnings_calendar"): completed(
            "nasdaq", "/get_earnings_calendar", {"unexpected": "shape"}, "0.01"
        ),
    })
    finance = make_service(client, tmp_path)

    response = await finance.get_earnings(limit=2)

    assert response["error"] == "schema_drift"
    ledger = summarize_ledger(tmp_path / "ledger.jsonl")
    assert ledger["calls"] == 1  # only the calendar call


@pytest.mark.asyncio
async def test_get_news_market_mode_message(tmp_path: Path) -> None:
    client = build_client()
    finance = make_service(client, tmp_path)

    response = await finance.get_news()

    assert response["error"] == "bad_request"
    assert response == {
        "error": "bad_request",
        "message": "market-wide news is not routed; pass ticker",
    }
    assert client.calls == []
