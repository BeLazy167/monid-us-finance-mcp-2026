"""Bounded TTL cache for Monid run results.

Repeat queries (the dominant pattern in demo traffic) are served from
memory in microseconds and cost nothing: no new Monid run, no wallet
spend, no ledger row. Fresh-data throughput stays bounded by the
client's run semaphore, so upstream Monid and the wallet are protected
while cached capacity scales to thousands of requests per second.
"""
from __future__ import annotations

import time
from collections import OrderedDict
from typing import Any

from monid_finance_mcp.client import MonidRun

DEFAULT_TTL_SECONDS = 300.0

# Per-endpoint TTL policy in seconds; fallback DEFAULT_TTL_SECONDS.
TTL_BY_ENDPOINT: dict[str, float] = {
    "/equities/v1/companies-list": 600.0,
    "/equities/v1/statements": 600.0,
    "/equities/v1/filings": 600.0,
    "/equities/v1/summary": 60.0,
    "/equities/v1/ohlcv": 60.0,
    "/news/search": 60.0,
    "/web/scrape/markdown": 3600.0,
    "/web/extract": 3600.0,
    "/web/search": 600.0,
    "/search": 600.0,
    "/get_stock_screener": 600.0,
    "/get_earnings_calendar": 300.0,
    "/get_institution_holders": 600.0,
}


def cache_ttl_for(endpoint: str) -> float:
    """TTL for one endpoint, exact match first then suffix rules."""
    if endpoint in TTL_BY_ENDPOINT:
        return TTL_BY_ENDPOINT[endpoint]
    return DEFAULT_TTL_SECONDS


class RunCache:
    """A bounded, asyncio-single-threaded LRU cache with per-entry TTL."""

    def __init__(self, *, max_entries: int = 512) -> None:
        if max_entries < 1:
            raise ValueError("max_entries must be at least 1")
        self._max_entries = max_entries
        self._entries: OrderedDict[Any, tuple[float, MonidRun]] = OrderedDict()
        self.hits = 0
        self.misses = 0

    def get(self, key: Any) -> MonidRun | None:
        entry = self._entries.get(key)
        if entry is None:
            self.misses += 1
            return None
        expires_at, run = entry
        if expires_at <= time.monotonic():
            del self._entries[key]
            self.misses += 1
            return None
        self._entries.move_to_end(key)
        self.hits += 1
        return run

    def put(self, key: Any, run: MonidRun, *, ttl_seconds: float) -> None:
        if ttl_seconds <= 0:
            return
        self._entries[key] = (time.monotonic() + ttl_seconds, run)
        self._entries.move_to_end(key)
        while len(self._entries) > self._max_entries:
            self._entries.popitem(last=False)

    def clear(self) -> None:
        self._entries.clear()

    def __len__(self) -> int:
        return len(self._entries)
