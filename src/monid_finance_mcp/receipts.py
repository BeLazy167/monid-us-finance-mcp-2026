from __future__ import annotations

import json
import math
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import TextIO, TypeGuard, cast

from monid_finance_mcp.client import MonidError, MonidRun
from monid_finance_mcp.models import JsonObject, JsonValue

DEFAULT_LEDGER_PATH = Path(__file__).resolve().parents[3] / "receipts" / "ledger.jsonl"


def _utc_now() -> str:
    return datetime.now(UTC).isoformat().replace("+00:00", "Z")


def _cost(run: MonidRun | None) -> JsonObject | None:
    if run is None or run.cost is None:
        return None
    return {"value": float(run.cost.value), "currency": run.cost.currency}


def _input_summary(*, body: JsonObject | None, query_params: JsonObject | None) -> JsonObject:
    summary: JsonObject = {}
    if query_params is not None:
        summary["query_params"] = dict(sorted(query_params.items()))
    if body is not None:
        summary["body"] = dict(sorted(body.items()))
    return summary


@dataclass(frozen=True, slots=True)
class Receipt:
    """One measured Monid call attempt, successful or failed."""

    tool: str
    provider: str
    endpoint: str
    run_id: str | None
    lifecycle_status: str | None
    provider_http_status: int | None
    measured_cost: JsonObject | None
    error: str | None
    input_summary: JsonObject

    def to_dict(self) -> JsonObject:
        record: JsonObject = {
            "timestamp": _utc_now(),
            "tool": self.tool,
            "provider": self.provider,
            "endpoint": self.endpoint,
        }
        if self.run_id is not None:
            record["run_id"] = self.run_id
        if self.lifecycle_status is not None:
            record["lifecycle_status"] = self.lifecycle_status
        if self.provider_http_status is not None:
            record["provider_http_status"] = self.provider_http_status
        if self.measured_cost is not None:
            record["measured_cost"] = self.measured_cost
        if self.error is not None:
            record["error"] = self.error
        record["input"] = self.input_summary
        return record


class ReceiptsLedger:
    """Append-only JSONL ledger of every Monid call, kept out of tool responses."""

    def __init__(self, path: Path = DEFAULT_LEDGER_PATH) -> None:
        self._path = path
        self._handle: TextIO | None = None

    def record_success(
        self,
        *,
        tool: str,
        run: MonidRun,
        body: JsonObject | None,
        query_params: JsonObject | None,
    ) -> None:
        self._append(
            Receipt(
                tool=tool,
                provider=run.provider,
                endpoint=run.endpoint,
                run_id=run.run_id,
                lifecycle_status=run.status,
                provider_http_status=run.provider_http_status,
                measured_cost=_cost(run),
                error=None,
                input_summary=_input_summary(body=body, query_params=query_params),
            )
        )

    def record_failure(
        self,
        *,
        tool: str,
        provider: str,
        endpoint: str,
        error: MonidError,
        body: JsonObject | None,
        query_params: JsonObject | None,
    ) -> None:
        run = error.run
        self._append(
            Receipt(
                tool=tool,
                provider=provider,
                endpoint=endpoint,
                run_id=run.run_id if run is not None else None,
                lifecycle_status=run.status if run is not None else None,
                provider_http_status=run.provider_http_status if run is not None else None,
                measured_cost=_cost(run),
                error=f"{type(error).__name__}: {error}",
                input_summary=_input_summary(body=body, query_params=query_params),
            )
        )

    def _append(self, receipt: Receipt) -> None:
        self._path.parent.mkdir(parents=True, exist_ok=True)
        if self._handle is None:
            self._handle = self._path.open("a", encoding="utf-8")
        self._handle.write(json.dumps(receipt.to_dict(), sort_keys=False) + "\n")
        self._handle.flush()

    def close(self) -> None:
        if self._handle is not None:
            self._handle.close()
            self._handle = None


def summarize_ledger(path: Path = DEFAULT_LEDGER_PATH) -> JsonObject:
    """Aggregate committed ledger rows for the cost story."""
    tools: dict[str, JsonObject] = {}
    total = 0.0
    calls = 0
    failures = 0
    if path.exists():
        for line in path.read_text(encoding="utf-8").splitlines():
            if not line.strip():
                continue
            loaded: object = json.loads(line)
            if not isinstance(loaded, dict):
                continue
            payload = cast(dict[object, object], loaded)
            row: JsonObject = {}
            for key, value in payload.items():
                if isinstance(key, str) and _is_json_value(value):
                    row[key] = value
            tool = row.get("tool")
            name = tool if isinstance(tool, str) else "unknown"
            calls += 1
            is_failure = row.get("error") is not None
            if is_failure:
                failures += 1
            cost = row.get("measured_cost")
            entry = tools.setdefault(name, {"calls": 0, "failures": 0, "usd_cost": 0.0})
            usd = _usd_value(cost)
            if usd is not None:
                total += usd
                running = _usd_value(entry.get("usd_cost"))
                entry["usd_cost"] = (running or 0.0) + usd
            calls_so_far = _int_value(entry.get("calls"))
            entry["calls"] = calls_so_far + 1
            if is_failure:
                failures_so_far = _int_value(entry.get("failures"))
                entry["failures"] = failures_so_far + 1
    return {
        "calls": calls,
        "failures": failures,
        "total_usd_cost": round(total, 6),
        "tools": dict(sorted(tools.items())),
    }


def _usd_value(cost: JsonValue) -> float | None:
    if not isinstance(cost, dict):
        return None
    currency = cost.get("currency")
    value = cost.get("value")
    if currency != "USD" or isinstance(value, bool) or not isinstance(value, int | float):
        return None
    return float(value)


def _int_value(value: JsonValue) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) else 0


def _is_json_value(value: object) -> TypeGuard[JsonValue]:
    if value is None or isinstance(value, str | int | bool):
        return True
    if isinstance(value, float):
        return math.isfinite(value)
    if isinstance(value, list):
        items = cast(list[object], value)
        return all(_is_json_value(item) for item in items)
    if isinstance(value, dict):
        mapping = cast(dict[object, object], value)
        return all(isinstance(key, str) and _is_json_value(item) for key, item in mapping.items())
    return False
