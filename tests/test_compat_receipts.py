from __future__ import annotations

import json
from decimal import Decimal
from pathlib import Path

import pytest

from monid_finance_mcp.client import MonidProviderHTTPError, MonidRun, MonidTimeoutError
from monid_finance_mcp.compat import (
    COMPAT_BASE_URL,
    CursorError,
    decode_cursor,
    encode_cursor,
    fd_error,
    paginate,
)
from monid_finance_mcp.models import JsonObject, Money
from monid_finance_mcp.receipts import ReceiptsLedger, summarize_ledger


def _run(provider: str = "defillama", endpoint: str = "/equities/v1/summary") -> MonidRun:
    return MonidRun(
        provider=provider,
        endpoint=endpoint,
        run_id="run-1",
        status="COMPLETED",
        output={"ok": True},
        provider_http_status=200,
        cost=Money(Decimal("0.0006")),
        created_at="2026-09-04T00:00:00Z",
        completed_at="2026-09-04T00:00:02Z",
        retrieved_at="2026-09-04T00:00:03Z",
    )


def test_fd_error_matches_documented_shape() -> None:
    assert fd_error("not_found", "No data found") == {
        "error": "not_found",
        "message": "No data found",
    }


def test_cursor_round_trip() -> None:
    cursor = encode_cursor(10, filters={"ticker": "AAPL"})
    offset, filters = decode_cursor(cursor)
    assert offset == 10
    assert filters == {"ticker": "AAPL"}


@pytest.mark.parametrize("cursor", ["!!!", "eyJvIjogLTF9", "e30", "bnVsbA"])
def test_cursor_rejects_malformed_tokens(cursor: str) -> None:
    with pytest.raises(CursorError):
        decode_cursor(cursor)


def test_paginate_emits_next_only_when_more_remain() -> None:
    records: list[JsonObject] = [{"i": i} for i in range(25)]
    first = paginate(records, offset=0, path="/filings")
    assert len(first.records) == 10
    assert first.next_cursor is not None
    assert first.next_url is not None
    assert first.next_url.startswith(COMPAT_BASE_URL + "/filings?cursor=")
    second_offset, _ = decode_cursor(first.next_cursor or "")
    second = paginate(records, offset=second_offset, path="/filings")
    assert second_offset == 10
    assert len(second.records) == 10
    third_offset, _ = decode_cursor(second.next_cursor or "")
    third = paginate(records, offset=third_offset, path="/filings")
    assert len(third.records) == 5
    assert third.next_cursor is None
    assert third.next_url is None


def test_ledger_records_success_and_failure(tmp_path: Path) -> None:
    ledger = ReceiptsLedger(tmp_path / "ledger.jsonl")
    run = _run()
    ledger.record_success(
        tool="get_stock_price", run=run, body=None, query_params={"ticker": "AAPL"}
    )
    failed = _run("nasdaq", "/get_stock_earnings")
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
        retrieved_at=failed.retrieved_at,
    )
    ledger.record_failure(
        tool="get_earnings",
        provider="nasdaq",
        endpoint="/get_stock_earnings",
        error=MonidProviderHTTPError(failed),
        body=None,
        query_params={"symbol": "AAPL"},
    )
    ledger.record_failure(
        tool="get_news",
        provider="context.dev",
        endpoint="/news/search",
        error=MonidTimeoutError("context.dev", "/news/search", "deadline exceeded"),
        body={"limit": 5},
        query_params=None,
    )
    ledger.close()

    rows = [json.loads(line) for line in (tmp_path / "ledger.jsonl").read_text().splitlines()]
    assert len(rows) == 3
    success, http_failure, timeout_failure = rows
    assert success["tool"] == "get_stock_price"
    assert success["run_id"] == "run-1"
    assert success["measured_cost"] == {"value": 0.0006, "currency": "USD"}
    assert success["lifecycle_status"] == "COMPLETED"
    assert "error" not in success
    assert http_failure["provider_http_status"] == 429
    assert "MonidProviderHTTPError" in http_failure["error"]
    assert timeout_failure.get("run_id") is None
    assert "MonidTimeoutError" in timeout_failure["error"]

    summary = summarize_ledger(tmp_path / "ledger.jsonl")
    assert summary["calls"] == 3
    assert summary["failures"] == 2
    assert summary["total_usd_cost"] == pytest.approx(0.0012)
    tools = summary["tools"]
    assert isinstance(tools, dict)
    earnings = tools["get_earnings"]
    price = tools["get_stock_price"]
    assert isinstance(earnings, dict) and isinstance(price, dict)
    assert earnings["failures"] == 1
    assert earnings["usd_cost"] == pytest.approx(0.0006)
    assert price["usd_cost"] == pytest.approx(0.0006)
