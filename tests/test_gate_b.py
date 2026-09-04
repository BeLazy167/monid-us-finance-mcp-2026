from __future__ import annotations

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


def _series(
    dates: list[str], rows: list[dict[str, int | float | None]], labels: list[str]
) -> JsonObject:
    return {
        "periodEnding": dates,
        "values": [[row.get(label) for row in rows] for label in labels],
    }


def statement_fixture() -> JsonObject:
    quarter_dates = [
        "2024-03-31",
        "2024-06-30",
        "2024-09-30",
        "2024-12-31",
        "2025-03-31",
        "2025-06-30",
        "2025-09-30",
        "2025-12-31",
        "2026-03-31",
    ]
    annual_dates = ["2024-12-31", "2025-12-31"]
    revenue_by_date = {
        "2024-03-31": 10,
        "2024-06-30": 20,
        "2024-09-30": 30,
        "2024-12-31": 40,
        "2025-03-31": 20,
        "2025-06-30": 40,
        "2025-09-30": 60,
        "2025-12-31": 80,
        "2026-03-31": 40,
    }

    def make_rows(dates: list[str], annual: bool) -> tuple[
        list[dict[str, int | float | None]],
        list[dict[str, int | float | None]],
        list[dict[str, int | float | None]],
    ]:
        income: list[dict[str, int | float | None]] = []
        balance: list[dict[str, int | float | None]] = []
        cash: list[dict[str, int | float | None]] = []
        for index, day in enumerate(dates):
            revenue = (100 if day == "2024-12-31" else 200) if annual else revenue_by_date[day]
            current_liabilities = 20 + index
            equity = 50 + index * 5
            assets = 100 + index * 10
            income.append(
                {
                    "Revenue": revenue,
                    "Cost of Revenue": revenue * 0.6,
                    "Gross Profit": revenue * 0.4,
                    "Operating Income": revenue * 0.2,
                    "Net Income": revenue * 0.1,
                    "EPS (Diluted)": revenue * 0.01,
                    "EPS (Basic)": revenue * 0.01,
                    "Shares Outstanding (Basic)": 10.0,
                    "Shares Outstanding (Diluted)": 11.0,
                    "EBIT": revenue * 0.25,
                    "EBITDA": revenue * 0.3,
                    "Non-Operating Interest Expense": revenue * 0.025,
                }
            )
            balance.append(
                {
                    "Total Current Assets": current_liabilities * 2,
                    "Total Current Liabilities": current_liabilities,
                    "Total Assets": assets,
                    "Total Shareholders Equity": equity,
                    "Total Liabilities": assets - equity,
                    "Cash and Cash Equivalents": current_liabilities * 0.5,
                    "Accounts Receivable": 10 + index,
                    "Inventory": 5 + index,
                    "Short-Term Debt": 3,
                    "Long-Term Debt": 7,
                }
            )
            cash.append(
                {
                    "Cash Flow from Operating Activities": revenue * 0.15,
                    "Cash Flow from Investing Activities": -(revenue * 0.05),
                    "Cash Flow from Financing Activities": -(revenue * 0.03),
                    "Capital Expenditure": -(revenue * 0.05),
                    "Free Cash Flow": revenue * 0.12,
                    "Net Cash Flow": revenue * 0.07,
                    "Common Dividends": -(revenue * 0.02),
                }
            )
        return income, balance, cash

    income_labels = [
        "Revenue",
        "Cost of Revenue",
        "Gross Profit",
        "Operating Income",
        "Net Income",
        "EPS (Diluted)",
        "EPS (Basic)",
        "Shares Outstanding (Basic)",
        "Shares Outstanding (Diluted)",
        "EBIT",
        "EBITDA",
    ]
    balance_labels = [
        "Total Current Assets",
        "Total Current Liabilities",
        "Total Assets",
        "Total Liabilities",
        "Total Shareholders Equity",
    ]
    cash_labels = [
        "Cash Flow from Operating Activities",
        "Cash Flow from Investing Activities",
        "Cash Flow from Financing Activities",
        "Free Cash Flow",
        "Net Cash Flow",
    ]

    def section_payload(
        dates: list[str], annual: bool
    ) -> tuple[JsonObject, JsonObject, JsonObject]:
        income, balance, cash = make_rows(dates, annual)
        income_block = _series(dates, income, income_labels)
        income_block["children"] = {
            "Non-Operating Items": {
                "values": [[row["Non-Operating Interest Expense"] for row in income]]
            }
        }
        balance_block = _series(dates, balance, balance_labels)
        balance_block["children"] = {
            "Total Current Assets": {
                "values": [
                    [row["Cash and Cash Equivalents"] for row in balance],
                    [row["Accounts Receivable"] for row in balance],
                    [row["Inventory"] for row in balance],
                ]
            },
            "Total Current Liabilities": {"values": [[row["Short-Term Debt"] for row in balance]]},
            "Total Non-Current Liabilities": {
                "values": [[row["Long-Term Debt"] for row in balance]]
            },
        }
        cash_block = _series(dates, cash, cash_labels)
        cash_block["children"] = {
            "Cash Flow from Investing Activities": {
                "values": [[row["Capital Expenditure"] for row in cash]]
            },
            "Cash Flow from Financing Activities": {
                "values": [[row["Common Dividends"] for row in cash]]
            },
        }
        return income_block, balance_block, cash_block

    q_income, q_balance, q_cash = section_payload(quarter_dates, False)
    a_income, a_balance, a_cash = section_payload(annual_dates, True)
    return {
        "incomeStatement": {
            "labels": income_labels,
            "children": {
                period: {"Non-Operating Items": {"labels": ["Non-Operating Interest Expense"]}}
                for period in ("annual", "quarterly")
            },
            "quarterly": q_income,
            "annual": a_income,
        },
        "balanceSheet": {
            "labels": balance_labels,
            "children": {
                period: {
                    "Total Current Assets": {
                        "labels": ["Cash and Cash Equivalents", "Accounts Receivable", "Inventory"]
                    },
                    "Total Current Liabilities": {"labels": ["Short-Term Debt"]},
                    "Total Non-Current Liabilities": {"labels": ["Long-Term Debt"]},
                }
                for period in ("annual", "quarterly")
            },
            "quarterly": q_balance,
            "annual": a_balance,
        },
        "cashflow": {
            "labels": cash_labels,
            "children": {
                period: {
                    "Cash Flow from Investing Activities": {"labels": ["Capital Expenditure"]},
                    "Cash Flow from Financing Activities": {"labels": ["Common Dividends"]},
                }
                for period in ("annual", "quarterly")
            },
            "quarterly": q_cash,
            "annual": a_cash,
        },
    }


def earnings_filings() -> list[JsonObject]:
    return [
        {
            "filingDate": "2026-01-20",
            "reportDate": "2025-12-31",
            "form": "10-Q",
            "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019326000001/aapl-20251231.htm",
        },
        {
            "filingDate": "2026-01-10",
            "reportDate": "2025-12-31",
            "form": "10-K",
            "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20251231.htm",
        },
        {
            "filingDate": "2025-11-01",
            "reportDate": "2025-09-30",
            "form": "10-Q",
            "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250930.htm",
        },
        {
            "filingDate": "2025-10-31",
            "reportDate": "2025-09-30",
            "form": "10-K",
            "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20250930.htm",
        },
        {
            "filingDate": "2025-01-15",
            "reportDate": "2024-12-31",
            "form": "10-K",
            "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019324000123/aapl-20241231.htm",
        },
    ]


INSIDER: JsonObject = {
    "status": "success",
    "data": {
        "query": "AAPL",
        "results": [
            {
                "transaction_date": "2026-09-01 Sale",
                "reported_datetime": "2026-09-03 6:30 pm",
                "company": "Apple Inc.",
                "symbol": "AAPL",
                "insider_relationship": "Newstead Jennifer SVP, GC",
                "shares_traded": "1,439",
                "average_price": "$317.01",
                "total_amount": "$456,177",
                "shares_owned": "35,790 (Direct)",
                "filing": "View",
                "filing_url": "https://www.secform4.com/filings/1.htm",
                "symbol_url": "https://www.secform4.com/#S2",
                "insider_relationship_url": "https://www.secform4.com/insider/1.htm",
            },
            {
                "transaction_date": "2026-08-25 Purchase",
                "reported_datetime": "2026-08-27 6:31 pm",
                "company": "Apple Inc.",
                "symbol": "AAPL",
                "insider_relationship": "Cook Timothy CEO",
                "shares_traded": "20,000",
                "average_price": "$300.00",
                "total_amount": "$6,000,000",
                "shares_owned": "3,000,000 (Indirect)",
                "filing": "View",
                "filing_url": "https://www.secform4.com/filings/2.htm",
                "symbol_url": "https://www.secform4.com/#S2",
                "insider_relationship_url": "https://www.secform4.com/insider/2.htm",
            },
        ],
    },
}

SCREENER: JsonObject = {
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
                        "name": "Apple Inc. Common Stock",
                        "lastsale": "$328.21",
                        "netchange": "-3.25",
                        "pctchange": "-1.00%",
                        "marketCap": "4,789,955,817,800",
                        "url": "/market-activity/stocks/aapl",
                    }
                ],
            },
            "totalrecords": 33,
            "asof": "Last price as of Sep 3, 2026",
        },
        "message": None,
        "status": {"rCode": 200, "bCodeMessage": None, "developerMessage": None},
    },
}


def gate_b_client() -> FakeClient:
    return FakeClient(
        {
            ("defillama", "/equities/v1/statements"): completed(
                "defillama", "/equities/v1/statements", statement_fixture(), "0.0006"
            ),
            ("defillama", "/equities/v1/filings"): completed(
                "defillama", "/equities/v1/filings", earnings_filings(), "0.0006"
            ),
            ("secform4", "/search"): completed("secform4", "/search", INSIDER, "0.01"),
            ("nasdaq", "/get_stock_screener"): completed(
                "nasdaq", "/get_stock_screener", SCREENER, "0.01"
            ),
        }
    )


@pytest.mark.asyncio
async def test_get_earnings_composes_required_fields(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.get_earnings(ticker="AAPL", limit=2)

    records = as_records(response["earnings"])
    assert len(records) == 2
    newest = records[0]
    assert newest["ticker"] == "AAPL"
    assert newest["report_period"] == "2025-12-31"
    assert newest["fiscal_period"] == "Q4 FY2025"
    assert newest["source_type"] == "10-Q"
    assert newest["filing_date"] == "2026-01-20"
    filing_url = newest["filing_url"]
    assert isinstance(filing_url, str)
    assert filing_url.startswith("https://www.sec.gov/Archives/")
    assert newest["accession_number"] == "0000320193-26-000001"
    required = {
        key
        for key, spec in FD_CONTRACT["EarningsRecord"].items()
        if spec.get("required")
    }
    assert required <= set(newest)
    quarterly = as_object(newest["quarterly"])
    assert quarterly["revenue"] == 80
    assert quarterly["revenue_chg"] == 20  # 80 - 60 (previous quarter)
    assert quarterly["revenue_yoy_chg"] == 40  # 80 - 40 (same quarter prior year)
    assert quarterly["earnings_per_share"] == pytest.approx(0.8)
    assert quarterly["gross_margin"] == pytest.approx(0.4)
    assert "gross_margin_chg_bps" in quarterly
    assert quarterly["total_assets"] == 170
    assert quarterly["free_cash_flow"] == pytest.approx(9.6)
    assert "annual" not in newest


@pytest.mark.asyncio
async def test_get_earnings_10k_includes_annual_block(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.get_earnings(ticker="AAPL", limit=4)

    records = as_records(response["earnings"])
    ten_k = [record for record in records if record["source_type"] == "10-K"]
    assert ten_k
    matched = [record for record in ten_k if "annual" in record]
    assert matched
    annual_ten_k = matched[0]
    assert annual_ten_k["report_period"] == "2025-12-31"
    assert annual_ten_k["filing_date"] == "2026-01-10"
    annual = as_object(annual_ten_k["annual"])
    assert annual["revenue"] == 200
    assert annual["revenue_chg"] == 100  # 200 - 100 prior year
    assert annual["revenue_yoy_chg"] == 100
    quarterly = as_object(annual_ten_k["quarterly"])
    assert quarterly["revenue"] == 80
    assert quarterly["revenue_chg"] == 20  # 80 - 60 previous quarter
    assert quarterly["revenue_yoy_chg"] == 40  # 80 - 40 same quarter prior year


@pytest.mark.asyncio
async def test_get_earnings_requires_ticker(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.get_earnings(limit=5)

    assert response["error"] == "bad_request"


@pytest.mark.asyncio
async def test_get_financial_metrics_matches_fd_contract(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.get_financial_metrics(ticker="AAPL", period="annual", limit=4)

    records = as_records(response["financial_metrics"])
    assert len(records) == 2
    newest = records[0]
    assert newest["report_period"] == "2025-12-31"
    assert newest["fiscal_period"] == "FY2025"
    assert newest["period"] == "annual"
    assert newest["gross_margin"] == pytest.approx(0.4)
    assert newest["operating_margin"] == pytest.approx(0.2)
    assert newest["net_margin"] == pytest.approx(0.1)
    assert newest["return_on_equity"] == pytest.approx(20 / 55)
    assert newest["current_ratio"] == pytest.approx(2.0)
    assert newest["debt_to_equity"] == pytest.approx(10 / 55)
    assert newest["interest_coverage"] == pytest.approx(0.25 / 0.025)
    assert newest["revenue_growth"] == pytest.approx(1.0)
    assert newest["earnings_per_share"] == pytest.approx(2.0)
    assert newest["book_value_per_share"] == pytest.approx(5.5)
    assert set(newest) <= set(FD_CONTRACT["FinancialMetricsResponse"])
    order = list(FD_CONTRACT["FinancialMetricsResponse"])
    positions = [order.index(key) for key in newest if key in order]
    assert positions == sorted(positions)
    # identity joined from filings (annual metrics join the 10-K)
    assert newest["accession_number"] == "0000320193-25-000079"
    assert newest["form_type"] == "10-K"
    assert newest["filing_date"] == "2026-01-10"


@pytest.mark.asyncio
async def test_get_financial_metrics_ttm_omits_identity(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.get_financial_metrics(ticker="AAPL", period="ttm", limit=4)

    records = as_records(response["financial_metrics"])
    assert records
    for record in records:
        assert "accession_number" not in record
        assert "form_type" not in record
        assert "filing_url" not in record
        assert "filing_date" not in record
        assert record["period"] == "ttm"


@pytest.mark.asyncio
async def test_get_financial_metrics_filing_date_filter(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.get_financial_metrics(
        ticker="AAPL", period="annual", filing_date_gte="2026-01-01"
    )

    records = as_records(response["financial_metrics"])
    assert [record["report_period"] for record in records] == ["2025-12-31"]


@pytest.mark.asyncio
async def test_get_insider_trades_matches_fd_contract(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.get_insider_trades(ticker="AAPL", limit=10)

    records = as_records(response["insider_trades"])
    assert len(records) == 2
    newest = records[0]
    assert newest["ticker"] == "AAPL"
    assert newest["issuer"] == "Apple Inc."
    assert newest["name"] == "Newstead Jennifer SVP, GC"
    assert newest["filing_date"] == "2026-09-03"
    assert newest["transaction_date"] == "2026-09-01"
    assert newest["transaction_type"] == "Sale"
    assert newest["transaction_shares"] == 1439
    assert newest["transaction_price_per_share"] == pytest.approx(317.01)
    assert newest["transaction_value"] == 456177
    assert newest["shares_owned_after_transaction"] == 35790
    assert set(newest) <= set(FD_CONTRACT["InsiderTrade"])
    order = list(FD_CONTRACT["InsiderTrade"])
    positions = [order.index(key) for key in newest if key in order]
    assert positions == sorted(positions)
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 1


@pytest.mark.asyncio
async def test_get_insider_trades_filters_and_rejections(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    filtered = await finance.get_insider_trades(
        ticker="AAPL", limit=10, name="cook", transaction_type="purchase"
    )
    records = as_records(filtered["insider_trades"])
    assert [record["name"] for record in records] == ["Cook Timothy CEO"]

    rejected = await finance.get_insider_trades(ticker="AAPL", form_type="4")
    assert rejected["error"] == "bad_request"
    no_ticker = await finance.get_insider_trades()
    assert no_ticker["error"] == "bad_request"


@pytest.mark.asyncio
async def test_screen_stocks_matches_fd_contract(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    response = await finance.screen_stocks(
        filters=[{"field": "exchange", "operator": "eq", "value": "NASDAQ"}], limit=10
    )

    records = as_records(response["search_results"])
    assert records == [
        {
            "ticker": "AAPL",
            "exchange": "NASDAQ",
            "market_cap": "4789955817800",
            "last_sale": "328.21",
            "net_change": "-3.25",
            "percent_change": "-0.01",
        }
    ]
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 1


@pytest.mark.asyncio
async def test_screen_stocks_rejects_unsupported_filters(tmp_path: Path) -> None:
    finance = make_service(gate_b_client(), tmp_path)

    unsupported = await finance.screen_stocks(
        filters=[{"field": "revenue", "operator": "gt", "value": 1000000000}]
    )
    assert unsupported["error"] == "bad_request"
    missing = await finance.screen_stocks()
    assert missing["error"] == "bad_request"
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 0


@pytest.mark.asyncio
async def test_list_stock_screener_filters_is_static_and_free(tmp_path: Path) -> None:
    client = gate_b_client()
    finance = make_service(client, tmp_path)

    response = await finance.list_stock_screener_filters()

    assert set(response) == {"metrics", "operators"}
    metrics = as_object(response["metrics"])
    company = as_records(metrics["company"])
    fields: set[str] = set()
    for entry in company:
        value = entry.get("field")
        if isinstance(value, str):
            fields.add(value)
    assert fields == {"exchange", "market_cap"}
    assert response["operators"] == ["eq"]
    assert client.calls == []
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 0


@pytest.mark.asyncio
async def test_gate_b_upstream_failure_maps_to_fd_error(tmp_path: Path) -> None:
    client = gate_b_client()
    run = completed("secform4", "/search", INSIDER, "0.01")
    from dataclasses import replace

    failed = replace(run, provider_http_status=503, status="PROVIDER_ERROR")
    client.outcomes[("secform4", "/search")] = MonidProviderHTTPError(failed)

    finance = make_service(client, tmp_path)
    response = await finance.get_insider_trades(ticker="AAPL")

    assert response["error"] == "upstream_error"
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["failures"] == 1
