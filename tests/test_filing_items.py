from __future__ import annotations

from dataclasses import dataclass, field
from decimal import Decimal
from typing import override

import pytest

from monid_finance_mcp.client import (
    MonidClientProtocol,
    MonidProviderHTTPError,
    MonidRun,
)
from monid_finance_mcp.models import JsonObject, JsonValue, Money
from monid_finance_mcp.providers.us.filing_items import (
    CATALOGS,
    parse_filing_sections,
    resolve_item,
)
from monid_finance_mcp.providers.us.normalize import InputError
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


def section_content(response: JsonObject, index: int = 0) -> str:
    sections = response["sections"]
    assert isinstance(sections, list)
    section = sections[index]
    assert isinstance(section, dict)
    content = section["content"]
    assert isinstance(content, str)
    return content


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


@pytest.mark.asyncio
async def test_get_filing_items_selects_body_not_table_of_contents() -> None:
    client = client_with(NOISY_10_K)

    response = await FinanceService(client).get_filing_items("aapl", "10-K", 2025, item="Item-1A")

    data = response["data"]
    assert isinstance(data, dict)
    content = section_content(data)
    assert "Demand, competition" in content
    assert "........ 12" not in content
    filing = data["filing"]
    assert isinstance(filing, dict)
    assert filing["accession_number"] == "0000320193-25-000079"
    assert response["total_cost"]["value"] == pytest.approx(0.0015)
    assert [item["run_id"] for item in response["provenance"]] == [
        "filing-run",
        "scrape-run",
    ]
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


def test_parser_returns_all_extractable_items_in_catalog_order() -> None:
    sections = parse_filing_sections(NOISY_10_K, "10-K", None)

    assert [section["item"] for section in sections] == ["Item-1", "Item-1A", "Item-2"]
    assert "body section" in section_content({"sections": sections}, 0)
    assert "owns and leases facilities" in section_content({"sections": sections}, 2)


def test_parser_never_returns_a_toc_only_candidate() -> None:
    markdown = (
        "# Table of Contents\n"
        "[Item 7. Management's Discussion and Analysis of Financial Condition and "
        "Results of Operations](#item-7)\n"
        "Navigation only.\n"
    )

    sections = parse_filing_sections(markdown, "10-K", resolve_item("10-K", "Item-7"))

    assert sections == []


@pytest.mark.asyncio
async def test_missing_requested_section_returns_typed_error_after_both_runs() -> None:
    client = client_with("# ITEM 1. BUSINESS\nOnly business appears.\n")

    response = await FinanceService(client).get_filing_items("AAPL", "10-K", 2025, item="Item-7")

    assert response["data"]["sections"] == []
    assert response["partial_errors"][0]["code"] == "section_not_found"
    assert len(response["provenance"]) == 2
    assert response["total_cost"]["value"] == pytest.approx(0.0015)


@pytest.mark.asyncio
async def test_invalid_args_and_unsupported_exhibits_never_spend() -> None:
    client = client_with(NOISY_10_K)
    service = FinanceService(client)

    bad_year = await service.get_filing_items("AAPL", "10-K", 1900)
    bad_quarter = await service.get_filing_items("AAPL", "10-K", 2025, quarter=5)
    bad_item = await service.get_filing_items("AAPL", "10-K", 2025, item="Item-99")
    bad_accession = await service.get_filing_items(
        "AAPL", "10-K", 2025, accession_number="not-an-accession"
    )
    exhibits = await service.get_filing_items("AAPL", "10-K", 2025, include_exhibits=True)

    assert client.calls == []
    assert all(
        response["partial_errors"][0]["code"] == "invalid_input"
        for response in (bad_year, bad_quarter, bad_item, bad_accession)
    )
    assert exhibits["partial_errors"][0]["code"] == "capability_unavailable"
    assert exhibits["total_cost"]["value"] == 0


@pytest.mark.asyncio
async def test_selected_non_sec_url_stops_before_scrape() -> None:
    client = client_with(
        NOISY_10_K,
        filings=[
            filing_row(
                url=("https://example.com/Archives/edgar/data/320193/000032019325000079/aapl.htm")
            )
        ],
    )

    response = await FinanceService(client).get_filing_items("AAPL", "10-K", 2025)

    assert response["partial_errors"][0]["code"] == "invalid_source_url"
    assert len(client.calls) == 1
    assert [item["run_id"] for item in response["provenance"]] == ["filing-run"]
    assert response["total_cost"]["value"] == pytest.approx(0.0006)


@pytest.mark.asyncio
async def test_context_route_failure_keeps_both_run_provenance_and_cost() -> None:
    client = client_with(NOISY_10_K)
    failed = completed(
        "context.dev",
        SCRAPE_ENDPOINT,
        {"error": "unavailable"},
        run_id="failed-scrape",
        http_status=503,
    )
    client.outcomes[("context.dev", SCRAPE_ENDPOINT)] = MonidProviderHTTPError(failed)

    response = await FinanceService(client).get_filing_items("AAPL", "10-K", 2025)

    assert response["data"]["sections"] == []
    assert response["partial_errors"][0]["code"] == "provider_http_error"
    assert [item["run_id"] for item in response["provenance"]] == [
        "filing-run",
        "failed-scrape",
    ]
    assert response["total_cost"]["value"] == pytest.approx(0.0015)


@pytest.mark.asyncio
async def test_filing_route_failure_returns_partial_provenance_without_scrape() -> None:
    client = client_with(NOISY_10_K)
    failed = completed(
        "defillama",
        FILINGS_ENDPOINT,
        {"error": "rate limited"},
        run_id="failed-index",
        http_status=429,
    )
    client.outcomes[("defillama", FILINGS_ENDPOINT)] = MonidProviderHTTPError(failed)

    response = await FinanceService(client).get_filing_items("AAPL", "10-K", 2025)

    assert response["partial_errors"][0]["code"] == "provider_http_error"
    assert [item["run_id"] for item in response["provenance"]] == ["failed-index"]
    assert len(client.calls) == 1


@pytest.mark.asyncio
async def test_accession_and_period_selection_are_deterministic() -> None:
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

    response = await FinanceService(client).get_filing_items(
        "AAPL",
        "10-Q",
        2025,
        quarter=1,
        item="Part-I-Item-1",
        accession_number="000032019325000010",
    )

    filing = response["data"]["filing"]
    assert isinstance(filing, dict)
    assert filing["report_date"] == "2025-03-29"
    assert filing["accession_number"] == "0000320193-25-000010"
    assert response["data"]["requested_item"] == "Part-I-Item-1"


def test_ten_q_repeated_item_numbers_require_explicit_part() -> None:
    with pytest.raises(InputError, match="ambiguous"):
        resolve_item("10-Q", "Item-1")

    assert resolve_item("10-Q", "Part I Item 1").name == "Part-I-Item-1"
    assert resolve_item("10-Q", "Part-1,Item-1").name == "Part-I-Item-1"
    assert resolve_item("10-Q", "Part-II-Item-1").name == "Part-II-Item-1"


@pytest.mark.asyncio
async def test_list_filing_item_types_is_static_source_cited_and_free() -> None:
    client = client_with(NOISY_10_K)
    service = FinanceService(client)

    response = await service.list_filing_item_types("10-Q")

    assert client.calls == []
    assert response["provenance"] == []
    assert response["total_cost"] == {"value": 0.0, "currency": "USD", "complete": True}
    catalogs = response["data"]["catalogs"]
    assert isinstance(catalogs, list)
    catalog = catalogs[0]
    assert isinstance(catalog, dict)
    assert catalog["catalog_version"] == "SEC-1296-02-25"
    assert catalog["form_revision"] == "SEC 1296 (02-25)"
    source = catalog["source"]
    assert isinstance(source, dict)
    assert source["url"] == "https://www.sec.gov/files/form10-q.pdf"
    scope = response["data"]["catalog_scope"]
    assert isinstance(scope, str)
    assert "no Monid upstream call or claim" in scope
    names = [item.name for item in CATALOGS["10-Q"].items]
    assert "Part-I-Item-1" in names
    assert "Part-II-Item-1" in names
    assert {catalog.catalog_version for catalog in CATALOGS.values()} == {
        "SEC-1673-02-25",
        "SEC-1296-02-25",
        "SEC-873-02-25",
    }


@pytest.mark.asyncio
async def test_list_filing_item_types_rejects_unknown_form_without_spend() -> None:
    client = client_with(NOISY_10_K)

    response = await FinanceService(client).list_filing_item_types("S-1")

    assert client.calls == []
    assert response["partial_errors"][0]["code"] == "invalid_input"
