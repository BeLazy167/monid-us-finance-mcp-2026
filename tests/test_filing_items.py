from __future__ import annotations

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
from monid_finance_mcp.providers.us.filing_items import (
    parse_filing_sections,
    resolve_item,
)
from monid_finance_mcp.providers.us.normalize import InputError
from monid_finance_mcp.receipts import ReceiptsLedger, summarize_ledger
from monid_finance_mcp.service import FinanceService

FILINGS_ENDPOINT = "/equities/v1/filings"
SCRAPE_ENDPOINT = "/web/scrape/markdown"
SEC_URL_1 = "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20250927.htm"
SEC_URL_2 = "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250329.htm"


def completed(
    provider: str,
    endpoint: str,
    output: JsonValue,
    *,
    run_id: str,
    http_status: int = 200,
) -> MonidRun:
    return MonidRun(
        provider=provider,
        endpoint=endpoint,
        run_id=run_id,
        status="COMPLETED",
        output=output,
        provider_http_status=http_status,
        cost=Money(value=Decimal("0.0009" if provider == "context.dev" else "0.0006")),
        created_at="2026-09-04T00:00:00Z",
        completed_at="2026-09-04T00:00:02Z",
        retrieved_at="2026-09-04T00:00:03Z",
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


def filing_row(
    *,
    report_date: str = "2025-09-27",
    filing_date: str = "2025-10-31",
    form: str = "10-K",
    url: str = SEC_URL_1,
) -> JsonObject:
    return {
        "filingDate": filing_date,
        "reportDate": report_date,
        "form": form,
        "primaryDocumentUrl": url,
        "documentDescription": f"{form} annual report",
    }


def markdown_payload(markdown: str, url: str = SEC_URL_1) -> JsonObject:
    return {
        "success": True,
        "markdown": markdown,
        "contentLength": len(markdown),
        "url": url,
        "metadata": {"title": "Apple 10-K"},
        "cache_metadata": {"hit": False},
    }


def client_with(markdown: str, filings: JsonValue | None = None) -> FakeClient:
    rows = [filing_row()] if filings is None else filings
    return FakeClient(
        {
            ("defillama", FILINGS_ENDPOINT): completed(
                "defillama", FILINGS_ENDPOINT, rows, run_id="filing-run"
            ),
            ("context.dev", SCRAPE_ENDPOINT): completed(
                "context.dev",
                SCRAPE_ENDPOINT,
                markdown_payload(markdown),
                run_id="scrape-run",
            ),
        }
    )


INLINE_XBRL_NOISE = (
    "<ix:hidden>\n"
    + (
        '<ix:nonNumeric name="dei:EntityFileNumber" contextRef="c-1">'
        "ITEM 1A. RISK FACTORS</ix:nonNumeric>\n" * 400
    )
    + "</ix:hidden>\n"
)
assert len(INLINE_XBRL_NOISE) > 32 * 1024

NOISY_10_K = (
    INLINE_XBRL_NOISE
    + "# Table of Contents\n"
    + "[Item 1. Business](#item-1)\n"
    + "[Item 1A. Risk Factors](#item-1a)\n"
    + "Item 2. Properties\n"
    + "# ITEM 1. BUSINESS\n"
    + "Apple designs and sells products. This is the body section.\n"
    + "# ITEM 1A. RISK FACTORS\n"
    + "Demand, competition, and supply constraints could harm results. "
    + "This sentence must be selected instead of the table of contents.\n"
    + "# ITEM 2. PROPERTIES\n"
    + "The company owns and leases facilities.\n"
)


def as_objects(value: object) -> list[JsonObject]:
    assert isinstance(value, list)
    items = cast(list[object], value)
    objects: list[JsonObject] = []
    for item in items:
        assert isinstance(item, dict)
        objects.append(cast(JsonObject, item))
    return objects


def make_service(client: FakeClient, tmp_path: Path) -> FinanceService:
    return FinanceService(client, ReceiptsLedger(tmp_path / "ledger.jsonl"))


@pytest.mark.asyncio
async def test_get_filing_items_matches_fd_response_shape(tmp_path: Path) -> None:
    client = client_with(NOISY_10_K)

    response = await make_service(client, tmp_path).get_filing_items(
        "aapl", "10-K", 2025, item="Item-1A"
    )

    assert set(response) == {"resource", "ticker", "filing_type", "accession_number",
                             "year", "quarter", "items"}
    assert response["ticker"] == "AAPL"
    assert response["filing_type"] == "10-K"
    assert response["accession_number"] == "0000320193-25-000079"
    assert response["year"] == 2025
    assert response["quarter"] is None
    items = as_objects(response["items"])
    assert len(items) == 1
    item = items[0]
    assert item["number"] == "Item-1A"
    assert item["name"] == "Risk Factors"
    text = item["text"]
    assert isinstance(text, str)
    assert "Demand, competition" in text
    assert "........ 12" not in text
    assert set(item) == {"number", "name", "text"}
    assert client.calls[1] == (
        "context.dev",
        SCRAPE_ENDPOINT,
        None,
        {
            "url": SEC_URL_1,
            "includeLinks": False,
            "includeImages": False,
            "useMainContentOnly": True,
            "timeoutMS": 30_000,
        },
    )


@pytest.mark.asyncio
async def test_parser_returns_all_extractable_items_in_catalog_order() -> None:
    sections = parse_filing_sections(NOISY_10_K, "10-K", None)
    assert [section["item"] for section in sections] == ["Item-1", "Item-1A", "Item-2"]
    assert [section["title"] for section in sections] == [
        "Business",
        "Risk Factors",
        "Properties",
    ]


def test_parser_never_returns_a_toc_only_candidate() -> None:
    sections = parse_filing_sections(NOISY_10_K, "10-K", None)
    for section in sections:
        content = section["content"]
        assert isinstance(content, str)
        assert "........ 12" not in content


@pytest.mark.asyncio
async def test_missing_requested_section_is_not_found_after_both_runs(tmp_path: Path) -> None:
    client = client_with("# ITEM 1. BUSINESS\nOnly business appears.\n")

    response = await make_service(client, tmp_path).get_filing_items(
        "AAPL", "10-K", 2025, item="Item-7"
    )

    assert response["error"] == "not_found"
    assert len(client.calls) == 2
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 2
    assert summary["failures"] == 0
    assert summary["total_usd_cost"] == pytest.approx(0.0015)


@pytest.mark.asyncio
async def test_invalid_args_and_unsupported_exhibits_never_spend(tmp_path: Path) -> None:
    client = client_with(NOISY_10_K)
    service = make_service(client, tmp_path)

    bad_year = await service.get_filing_items("AAPL", "10-K", 1900)
    bad_quarter = await service.get_filing_items("AAPL", "10-K", 2025, quarter=5)
    bad_item = await service.get_filing_items("AAPL", "10-K", 2025, item="Item-99")
    bad_accession = await service.get_filing_items(
        "AAPL", "10-K", 2025, accession_number="not-an-accession"
    )
    exhibits = await service.get_filing_items("AAPL", "10-K", 2025, include_exhibits=True)

    assert client.calls == []
    for response in (bad_year, bad_quarter, bad_item, bad_accession, exhibits):
        assert response["error"] == "bad_request"
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 0


@pytest.mark.asyncio
async def test_selected_non_sec_url_stops_before_scrape(tmp_path: Path) -> None:
    client = client_with(
        NOISY_10_K,
        filings=[
            filing_row(
                url=("https://example.com/Archives/edgar/data/320193/000032019325000079/aapl.htm")
            )
        ],
    )

    response = await make_service(client, tmp_path).get_filing_items("AAPL", "10-K", 2025)

    assert response["error"] == "upstream_error"
    assert len(client.calls) == 1
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 1
    assert summary["total_usd_cost"] == pytest.approx(0.0006)


@pytest.mark.asyncio
async def test_context_route_failure_maps_to_fd_error(tmp_path: Path) -> None:
    client = client_with(NOISY_10_K)
    failed = completed(
        "context.dev",
        SCRAPE_ENDPOINT,
        {"error": "unavailable"},
        run_id="failed-scrape",
        http_status=503,
    )
    client.outcomes[("context.dev", SCRAPE_ENDPOINT)] = MonidProviderHTTPError(failed)

    response = await make_service(client, tmp_path).get_filing_items("AAPL", "10-K", 2025)

    assert response["error"] == "upstream_error"
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 2
    assert summary["failures"] == 1
    assert summary["total_usd_cost"] == pytest.approx(0.0015)


@pytest.mark.asyncio
async def test_filing_route_failure_maps_to_fd_error(tmp_path: Path) -> None:
    client = client_with(NOISY_10_K)
    failed = completed(
        "defillama",
        FILINGS_ENDPOINT,
        {"error": "rate limited"},
        run_id="failed-index",
        http_status=429,
    )
    client.outcomes[("defillama", FILINGS_ENDPOINT)] = MonidProviderHTTPError(failed)

    response = await make_service(client, tmp_path).get_filing_items("AAPL", "10-K", 2025)

    assert response["error"] == "upstream_error"
    assert len(client.calls) == 1
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["failures"] == 1


@pytest.mark.asyncio
async def test_accession_and_period_selection_are_deterministic(tmp_path: Path) -> None:
    filings = [
        filing_row(report_date="2025-03-29", filing_date="2025-05-01", form="10-Q", url=SEC_URL_2),
        filing_row(report_date="2025-09-27", filing_date="2025-10-31", form="10-Q", url=SEC_URL_1),
        filing_row(report_date="2024-09-28", filing_date="2024-11-01", form="10-Q", url=SEC_URL_1),
    ]
    markdown = (
        "# PART I\n# ITEM 1. FINANCIAL STATEMENTS\nQuarterly statements.\n"
        "# ITEM 2. MANAGEMENT'S DISCUSSION AND ANALYSIS OF FINANCIAL CONDITION AND "
        "RESULTS OF OPERATIONS\nMD&A body.\n"
        "# PART II\n# ITEM 1. LEGAL PROCEEDINGS\nNo material proceedings.\n"
    )
    client = client_with(markdown, filings=filings)
    client.outcomes[("context.dev", SCRAPE_ENDPOINT)] = completed(
        "context.dev",
        SCRAPE_ENDPOINT,
        markdown_payload(markdown, SEC_URL_2),
        run_id="scrape-run",
    )

    response = await make_service(client, tmp_path).get_filing_items(
        "AAPL",
        "10-Q",
        2025,
        quarter=1,
        item="Part-I-Item-1",
        accession_number="000032019325000010",
    )

    assert response["accession_number"] == "0000320193-25-000010"
    assert response["year"] == 2025
    assert response["quarter"] == 1
    items = as_objects(response["items"])
    assert len(items) == 1
    assert items[0]["number"] == "Part-I-Item-1"


def test_ten_q_repeated_item_numbers_require_explicit_part() -> None:
    with pytest.raises(InputError, match="ambiguous"):
        resolve_item("10-Q", "Item-1")

    assert resolve_item("10-Q", "Part I Item 1").name == "Part-I-Item-1"
    assert resolve_item("10-Q", "Part-1,Item-1").name == "Part-I-Item-1"
    assert resolve_item("10-Q", "Part-II-Item-1").name == "Part-II-Item-1"


@pytest.mark.asyncio
async def test_list_filing_item_types_is_static_and_free(tmp_path: Path) -> None:
    client = client_with(NOISY_10_K)
    service = make_service(client, tmp_path)

    response = await service.list_filing_item_types("10-Q")

    assert client.calls == []
    entries = as_objects(response["10-Q"])
    names = [entry["name"] for entry in entries]
    assert "Part-I-Item-1" in names
    assert "Part-II-Item-1" in names
    assert all(set(entry) == {"name", "title", "description"} for entry in entries)
    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 0


@pytest.mark.asyncio
async def test_list_filing_item_types_without_filter_keys_all_forms(tmp_path: Path) -> None:
    client = client_with(NOISY_10_K)
    service = make_service(client, tmp_path)

    response = await service.list_filing_item_types(None)

    assert set(response) == {"10-K", "10-Q", "8-K"}
    assert client.calls == []


@pytest.mark.asyncio
async def test_list_filing_item_types_rejects_unknown_form_without_spend(tmp_path: Path) -> None:
    client = client_with(NOISY_10_K)

    response = await make_service(client, tmp_path).list_filing_item_types("S-1")

    assert response["error"] == "bad_request"
    assert client.calls == []
