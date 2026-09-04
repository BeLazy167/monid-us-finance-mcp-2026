from __future__ import annotations

import asyncio
import json
import os
import sys
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime
from decimal import Decimal
from pathlib import Path

import pytest

from monid_finance_mcp.client import (
    AsyncCommandRunner,
    CommandResult,
    CommandTimeoutError,
    MonidClient,
    MonidProviderHTTPError,
    MonidRunError,
    MonidSchemaError,
    MonidTimeoutError,
)
from monid_finance_mcp.models import JsonValue, Money


@dataclass
class FakeRunner:
    responses: deque[CommandResult | Exception]
    calls: list[tuple[tuple[str, ...], float]] = field(
        default_factory=lambda: list[tuple[tuple[str, ...], float]]()
    )

    async def run(self, args: tuple[str, ...], timeout_seconds: float) -> CommandResult:
        self.calls.append((args, timeout_seconds))
        response = self.responses.popleft()
        if isinstance(response, Exception):
            raise response
        return response


@dataclass
class FakeFetcher:
    response: JsonValue
    urls: list[str] = field(default_factory=lambda: list[str]())

    async def fetch_json(self, url: str, timeout_seconds: float) -> JsonValue:
        self.urls.append(url)
        return self.response


class YieldingRunner(FakeRunner):
    async def run(self, args: tuple[str, ...], timeout_seconds: float) -> CommandResult:
        await asyncio.sleep(0)
        return await super().run(args, timeout_seconds)


class HangingFetcher:
    async def fetch_json(self, url: str, timeout_seconds: float) -> JsonValue:
        del url, timeout_seconds
        await asyncio.Future[None]()
        return None


class RecordingFetcher:
    completed_at: datetime | None = None

    async def fetch_json(self, url: str, timeout_seconds: float) -> JsonValue:
        del url, timeout_seconds
        await asyncio.sleep(0.01)
        self.completed_at = datetime.now().astimezone()
        return {"data": []}


def inspect_response(endpoint: str) -> CommandResult:
    return CommandResult(
        exit_code=0,
        stdout=json.dumps({"provider": "defillama", "endpoint": endpoint, "input": {}}),
        stderr="",
    )


def run_response(
    endpoint: str,
    *,
    output: JsonValue,
    status: str = "COMPLETED",
    http_status: int = 200,
) -> CommandResult:
    return CommandResult(
        exit_code=0,
        stdout=json.dumps(
            {
                "runId": "run-1",
                "provider": "defillama",
                "endpoint": endpoint,
                "status": status,
                "output": output,
                "providerResponse": {"httpStatus": http_status},
                "billing": {
                    "reportedCost": {
                        "currency": "USD",
                        "value": 600,
                        "unit": "MICRO_DOLLAR",
                    }
                },
                "createdAt": "2026-09-04T00:00:00Z",
                "completedAt": "2026-09-04T00:00:02Z",
            }
        ),
        stderr="",
    )


@pytest.mark.asyncio
async def test_client_inspects_immediately_before_waiting_run() -> None:
    endpoint = "/equities/v1/summary"
    runner = FakeRunner(
        deque([inspect_response(endpoint), run_response(endpoint, output={"price": 1})])
    )
    client = MonidClient(runner=runner, run_timeout_seconds=12)

    result = await client.run(
        "defillama",
        endpoint,
        query_params={"ticker": "AAPL", "country": "US"},
    )

    assert result.run_id == "run-1"
    assert result.cost is not None
    assert result.cost.value == Decimal("0.0006")
    assert result.output == {"price": 1}
    assert [call[0][1] for call in runner.calls] == ["inspect", "run"]
    inspect_args, run_args = runner.calls[0][0], runner.calls[1][0]
    assert inspect_args[:3] == ("monid", "inspect", "--json")
    assert run_args[:5] == ("monid", "run", "--json", "--wait", "12")
    assert run_args[run_args.index("--query") + 1] == '{"country":"US","ticker":"AAPL"}'


@pytest.mark.asyncio
async def test_client_checks_provider_http_status_after_lifecycle() -> None:
    endpoint = "/equities/v1/summary"
    runner = FakeRunner(
        deque(
            [
                inspect_response(endpoint),
                run_response(endpoint, output={"error": "slow down"}, http_status=429),
            ]
        )
    )

    with pytest.raises(MonidProviderHTTPError) as caught:
        await MonidClient(runner=runner).run("defillama", endpoint)

    assert caught.value.run is not None
    assert caught.value.run.provider_http_status == 429
    assert caught.value.run.cost is not None
    assert caught.value.run.cost.value == Decimal("0.0006")


@pytest.mark.asyncio
async def test_client_rejects_terminal_failure_state() -> None:
    endpoint = "/equities/v1/summary"
    runner = FakeRunner(
        deque([inspect_response(endpoint), run_response(endpoint, output=None, status="TIMED_OUT")])
    )

    with pytest.raises(MonidRunError) as caught:
        await MonidClient(runner=runner).run("defillama", endpoint)

    assert caught.value.run is not None
    assert caught.value.run.status == "TIMED_OUT"


@pytest.mark.asyncio
async def test_client_applies_bounded_run_timeout() -> None:
    endpoint = "/equities/v1/summary"
    runner = FakeRunner(deque([inspect_response(endpoint), CommandTimeoutError("timed out")]))

    with pytest.raises(MonidTimeoutError):
        await MonidClient(runner=runner, run_timeout_seconds=3).run("defillama", endpoint)

    assert runner.calls[-1][1] == pytest.approx(5.0)


@pytest.mark.asyncio
async def test_client_reports_schema_drift() -> None:
    endpoint = "/equities/v1/summary"
    malformed = CommandResult(
        exit_code=0,
        stdout=json.dumps(
            {
                "runId": "run-1",
                "status": "COMPLETED",
                "output": {},
                "providerResponse": {},
                "billing": {
                    "reportedCost": {
                        "currency": "USD",
                        "value": 600,
                        "unit": "MICRO_DOLLAR",
                    }
                },
                "createdAt": "2026-09-04T00:00:00Z",
                "completedAt": "2026-09-04T00:00:02Z",
            }
        ),
        stderr="",
    )
    runner = FakeRunner(deque([inspect_response(endpoint), malformed]))

    with pytest.raises(MonidSchemaError, match=r"providerResponse\.httpStatus"):
        await MonidClient(runner=runner).run("defillama", endpoint)


@pytest.mark.asyncio
async def test_client_preserves_empty_output() -> None:
    endpoint = "/equities/v1/filings"
    runner = FakeRunner(deque([inspect_response(endpoint), run_response(endpoint, output=[])]))

    result = await MonidClient(runner=runner).run("defillama", endpoint)

    assert result.output == []
    assert result.is_empty


@pytest.mark.asyncio
async def test_client_hydrates_signed_json_without_dropping_wrapper() -> None:
    endpoint = "/equities/v1/companies-list"
    descriptor = {
        "data": {
            "download_link": "https://sfs.monid.ai/signed",
            "content_type": "application/json",
        }
    }
    runner = FakeRunner(
        deque([inspect_response(endpoint), run_response(endpoint, output=descriptor)])
    )
    fetcher = FakeFetcher({"data": [{"ticker": "AAPL", "country": "US"}]})

    result = await MonidClient(runner=runner, artifact_fetcher=fetcher).run("defillama", endpoint)

    assert result.output == {"data": {"data": [{"ticker": "AAPL", "country": "US"}]}}
    assert fetcher.urls == ["https://sfs.monid.ai/signed"]


@pytest.mark.asyncio
async def test_client_runs_inspect_before_run_per_call_under_concurrency() -> None:
    """Concurrent client.run calls interleave, but each call still executes
    its own inspect before its own run for the same endpoint."""
    first = "/equities/v1/summary"
    second = "/equities/v1/filings"
    responses: dict[str, deque[CommandResult]] = {
        first: deque([inspect_response(first), run_response(first, output={"currentPrice": 1})]),
        second: deque([inspect_response(second), run_response(second, output=[])]),
    }
    calls: list[tuple[str, str]] = []

    class PerEndpointRunner:
        async def run(self, args: tuple[str, ...], timeout_seconds: float) -> CommandResult:
            del timeout_seconds
            operation = args[1]
            endpoint = args[args.index("--endpoint") + 1]
            calls.append((operation, endpoint))
            queue = responses[endpoint]
            result = queue.popleft()
            await asyncio.sleep(0)
            return result

    client = MonidClient(runner=PerEndpointRunner())

    await asyncio.gather(
        client.run("defillama", first),
        client.run("defillama", second),
    )

    for endpoint in (first, second):
        order = [operation for operation, seen in calls if seen == endpoint]
        assert order == ["inspect", "run"], (endpoint, order)


@pytest.mark.asyncio
async def test_missing_cost_keeps_run_metadata_and_marks_cost_unknown() -> None:
    endpoint = "/equities/v1/summary"
    raw = json.loads(run_response(endpoint, output={"currentPrice": 1}).stdout)
    del raw["billing"]
    missing_cost = CommandResult(0, json.dumps(raw), "")
    runner = FakeRunner(deque([inspect_response(endpoint), missing_cost]))

    with pytest.raises(MonidSchemaError, match="measured billing cost") as caught:
        await MonidClient(runner=runner).run("defillama", endpoint)

    assert caught.value.run is not None
    assert caught.value.run.run_id == "run-1"
    assert caught.value.run.cost is None
    assert caught.value.run.created_at == "2026-09-04T00:00:00Z"


@pytest.mark.asyncio
async def test_artifact_download_has_wall_clock_timeout_and_keeps_run() -> None:
    endpoint = "/equities/v1/companies-list"
    descriptor = {
        "data": {
            "download_link": "https://sfs.monid.ai/signed",
            "content_type": "application/json",
        }
    }
    runner = FakeRunner(
        deque([inspect_response(endpoint), run_response(endpoint, output=descriptor)])
    )

    with pytest.raises(MonidTimeoutError, match="artifact download") as caught:
        await MonidClient(
            runner=runner,
            artifact_fetcher=HangingFetcher(),
            artifact_timeout_seconds=0.01,
        ).run("defillama", endpoint)

    assert caught.value.run is not None
    assert caught.value.run.run_id == "run-1"


@pytest.mark.asyncio
async def test_command_runner_kills_process_when_task_is_cancelled(tmp_path: Path) -> None:
    pid_file = tmp_path / "pid"
    script = (
        "import os,pathlib,time;"
        f"pathlib.Path({str(pid_file)!r}).write_text(str(os.getpid()));"
        "time.sleep(30)"
    )
    task = asyncio.create_task(
        AsyncCommandRunner().run((sys.executable, "-c", script), timeout_seconds=40)
    )
    for _ in range(100):
        if pid_file.exists():
            break
        await asyncio.sleep(0.01)
    assert pid_file.exists()
    pid = int(pid_file.read_text())

    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task

    with pytest.raises(ProcessLookupError):
        os.kill(pid, 0)


@pytest.mark.asyncio
@pytest.mark.skipif(os.name != "posix", reason="process groups require POSIX")
async def test_command_runner_kills_sigterm_ignoring_descendants(tmp_path: Path) -> None:
    pid_file = tmp_path / "pids"
    child_script = "import signal,time;signal.signal(signal.SIGTERM, signal.SIG_IGN);time.sleep(30)"
    parent_script = (
        "import os,pathlib,subprocess,sys,time;"
        f"child=subprocess.Popen([sys.executable,'-c',{child_script!r}]);"
        f"pathlib.Path({str(pid_file)!r}).write_text(f'{{os.getpid()}} {{child.pid}}');"
        "time.sleep(30)"
    )
    task = asyncio.create_task(
        AsyncCommandRunner().run((sys.executable, "-c", parent_script), timeout_seconds=40)
    )
    for _ in range(100):
        if pid_file.exists():
            break
        await asyncio.sleep(0.01)
    assert pid_file.exists()
    group_id = int(pid_file.read_text().split()[0])

    task.cancel()
    with pytest.raises(asyncio.CancelledError):
        await task

    group_exists = True
    for _ in range(100):
        try:
            os.killpg(group_id, 0)
        except ProcessLookupError:
            group_exists = False
            break
        await asyncio.sleep(0.01)
    assert not group_exists


@pytest.mark.asyncio
async def test_missing_status_keeps_known_run_id_and_cost() -> None:
    endpoint = "/equities/v1/summary"
    raw = json.loads(run_response(endpoint, output={"currentPrice": 1}).stdout)
    del raw["status"]
    missing_status = CommandResult(0, json.dumps(raw), "")
    runner = FakeRunner(deque([inspect_response(endpoint), missing_status]))

    with pytest.raises(MonidSchemaError, match="omitted status") as caught:
        await MonidClient(runner=runner).run("defillama", endpoint)

    assert caught.value.run is not None
    assert caught.value.run.run_id == "run-1"
    assert caught.value.run.cost == Money(Decimal("0.0006"))


@pytest.mark.asyncio
async def test_retrieved_at_is_after_artifact_download() -> None:
    endpoint = "/equities/v1/companies-list"
    descriptor = {
        "data": {
            "download_link": "https://sfs.monid.ai/signed",
            "content_type": "application/json",
        }
    }
    runner = FakeRunner(
        deque([inspect_response(endpoint), run_response(endpoint, output=descriptor)])
    )
    fetcher = RecordingFetcher()

    result = await MonidClient(runner=runner, artifact_fetcher=fetcher).run("defillama", endpoint)

    assert fetcher.completed_at is not None
    assert result.retrieved_at is not None
    retrieved_at = datetime.fromisoformat(result.retrieved_at.replace("Z", "+00:00"))
    assert retrieved_at >= fetcher.completed_at
