
from __future__ import annotations

from decimal import Decimal

from monid_finance_mcp.cache import DEFAULT_TTL_SECONDS, RunCache, cache_ttl_for
from monid_finance_mcp.client import MonidRun
from monid_finance_mcp.models import Money


def run(run_id: str) -> MonidRun:
    return MonidRun(
        provider="defillama",
        endpoint="/equities/v1/summary",
        run_id=run_id,
        status="COMPLETED",
        output={"currentPrice": 1.0},
        provider_http_status=200,
        cost=Money(value=Decimal("0.0006")),
        created_at="2026-09-04T00:00:00Z",
        completed_at="2026-09-04T00:00:02Z",
    )


def test_cache_round_trip_and_stats() -> None:
    cache = RunCache()
    original = run("r1")
    key = ("defillama", "/equities/v1/summary", None, '{"ticker":"AAPL"}')
    cache.put(key, original, ttl_seconds=60)
    cached = cache.get(key)
    assert cached is not None and cached.run_id == "r1"
    assert cache.hits == 1
    other = ("defillama", "/equities/v1/summary", None, '{"ticker":"MSFT"}')
    assert cache.get(other) is None
    assert cache.misses == 1


def test_cache_expiry() -> None:
    cache = RunCache()
    cache.put("k", run("r2"), ttl_seconds=0)
    assert cache.get("k") is None
    cache.put("k", run("r3"), ttl_seconds=60)
    assert cache.get("k") is not None


def test_cache_bounds_entries() -> None:
    cache = RunCache(max_entries=2)
    cache.put("a", run("a"), ttl_seconds=60)
    cache.put("b", run("b"), ttl_seconds=60)
    cache.get("a")  # refresh a so b becomes LRU
    cache.put("c", run("c"), ttl_seconds=60)
    assert len(cache) == 2
    assert cache.get("b") is None
    assert cache.get("a") is not None
    assert cache.get("c") is not None


def test_ttl_policy() -> None:
    assert cache_ttl_for("/equities/v1/summary") == 60.0
    assert cache_ttl_for("/web/extract") == 3600.0
    assert cache_ttl_for("/unknown/endpoint") == DEFAULT_TTL_SECONDS
