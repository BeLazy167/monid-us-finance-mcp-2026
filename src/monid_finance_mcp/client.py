from __future__ import annotations

import asyncio
import json
import math
import os
import signal
from contextlib import suppress
from dataclasses import dataclass, field, replace
from datetime import UTC, datetime
from decimal import Decimal
from pathlib import Path
from typing import Protocol, cast
from urllib.parse import urlparse

import httpx

from monid_finance_mcp.models import JsonObject, JsonValue, Money

TERMINAL_STATUSES = frozenset(
    {"COMPLETED", "FAILED", "BLOCKED", "STOPPED", "TIME_OUT", "TIMED_OUT"}
)


@dataclass(frozen=True, slots=True)
class CommandResult:
    exit_code: int
    stdout: str
    stderr: str


class CommandTimeoutError(Exception):
    """The subprocess exceeded its local deadline."""


class CommandRunner(Protocol):
    async def run(self, args: tuple[str, ...], timeout_seconds: float) -> CommandResult: ...


class ArtifactFetcher(Protocol):
    async def fetch_json(self, url: str, timeout_seconds: float) -> JsonValue: ...


@dataclass(frozen=True, slots=True)
class MonidRun:
    provider: str
    endpoint: str
    run_id: str
    status: str
    output: JsonValue
    provider_http_status: int | None
    cost: Money | None
    created_at: str | None
    completed_at: str | None
    retrieved_at: str | None = None

    @property
    def is_empty(self) -> bool:
        return self.output is None or self.output == "" or self.output == [] or self.output == {}


class MonidClientProtocol(Protocol):
    async def run(
        self,
        provider: str,
        endpoint: str,
        *,
        body: JsonObject | None = None,
        query_params: JsonObject | None = None,
        path_params: JsonObject | None = None,
    ) -> MonidRun: ...


class MonidError(Exception):
    def __init__(
        self,
        provider: str,
        endpoint: str,
        message: str,
        *,
        run: MonidRun | None = None,
    ) -> None:
        super().__init__(message)
        self.provider = provider
        self.endpoint = endpoint
        self.run = run


class MonidTimeoutError(MonidError):
    """A bounded inspect, run, or artifact request timed out."""


class MonidInvocationError(MonidError):
    """The CLI process failed before it returned a run."""


class MonidSchemaError(MonidError):
    """Monid or provider data no longer matches the required contract."""


class MonidRunError(MonidError):
    def __init__(self, run: MonidRun) -> None:
        super().__init__(
            run.provider,
            run.endpoint,
            f"Monid run ended with {run.status}.",
            run=run,
        )


class MonidProviderHTTPError(MonidError):
    def __init__(self, run: MonidRun) -> None:
        super().__init__(
            run.provider,
            run.endpoint,
            f"Provider returned HTTP {run.provider_http_status}.",
            run=run,
        )


class AsyncCommandRunner:
    async def run(self, args: tuple[str, ...], timeout_seconds: float) -> CommandResult:
        process = await asyncio.create_subprocess_exec(
            *args,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            start_new_session=True,
        )
        try:
            stdout, stderr = await asyncio.wait_for(process.communicate(), timeout=timeout_seconds)
        except TimeoutError as error:
            await _terminate_process_group(process)
            raise CommandTimeoutError(f"Command exceeded {timeout_seconds:g} seconds.") from error
        except asyncio.CancelledError:
            await _terminate_process_group(process)
            raise
        if process.returncode is None:
            raise RuntimeError("Subprocess ended without a return code.")
        return CommandResult(
            exit_code=process.returncode,
            stdout=stdout.decode("utf-8", errors="replace"),
            stderr=stderr.decode("utf-8", errors="replace"),
        )


@dataclass(frozen=True, slots=True)
class HttpxArtifactFetcher:
    max_bytes: int = 20 * 1024 * 1024

    async def fetch_json(self, url: str, timeout_seconds: float) -> JsonValue:
        parsed = urlparse(url)
        if parsed.scheme != "https" or parsed.hostname != "sfs.monid.ai":
            raise ValueError("Monid artifact URL must use https://sfs.monid.ai.")
        content = bytearray()
        async with (
            httpx.AsyncClient(timeout=timeout_seconds, follow_redirects=False) as client,
            client.stream("GET", url) as response,
        ):
            response.raise_for_status()
            async for chunk in response.aiter_bytes():
                content.extend(chunk)
                if len(content) > self.max_bytes:
                    raise ValueError(f"Monid artifact exceeded {self.max_bytes} bytes.")
        return _parse_json_value(content.decode("utf-8"), "artifact")


@dataclass(slots=True)
class MonidClient:
    cli: str = "monid"
    run_timeout_seconds: float = 90
    inspect_timeout_seconds: float = 15
    artifact_timeout_seconds: float = 15
    runner: CommandRunner | None = None
    artifact_fetcher: ArtifactFetcher | None = None
    allowlist_path: Path | None = None
    max_concurrent_runs: int = 8
    _run_slots: asyncio.Semaphore = field(init=False, repr=False)
    _allowlist: frozenset[tuple[str, str]] | None = field(
        init=False, repr=False, default=None
    )

    def __post_init__(self) -> None:
        for name, value in (
            ("run_timeout_seconds", self.run_timeout_seconds),
            ("inspect_timeout_seconds", self.inspect_timeout_seconds),
            ("artifact_timeout_seconds", self.artifact_timeout_seconds),
        ):
            if not math.isfinite(value) or value <= 0 or value > 300:
                raise ValueError(f"{name} must be greater than 0 and at most 300.")
        if not math.isfinite(self.max_concurrent_runs) or self.max_concurrent_runs < 1:
            raise ValueError("max_concurrent_runs must be at least 1.")
        self._run_slots = asyncio.Semaphore(self.max_concurrent_runs)
        if self.runner is None:
            self.runner = AsyncCommandRunner()
        if self.artifact_fetcher is None:
            self.artifact_fetcher = HttpxArtifactFetcher()

    async def run(
        self,
        provider: str,
        endpoint: str,
        *,
        body: JsonObject | None = None,
        query_params: JsonObject | None = None,
        path_params: JsonObject | None = None,
    ) -> MonidRun:
        async with self._run_slots:
            return await self._run_locked(
                provider,
                endpoint,
                body=body,
                query_params=query_params,
                path_params=path_params,
            )

    def _allowlist_permits(self, provider: str, endpoint: str) -> bool:
        if self.allowlist_path is None:
            return False
        if self._allowlist is None:
            try:
                loaded: object = json.loads(self.allowlist_path.read_text(encoding="utf-8"))
            except (OSError, json.JSONDecodeError):
                self._allowlist = frozenset()
                return False
            pairs: set[tuple[str, str]] = set()
            if isinstance(loaded, dict):
                document = cast(JsonObject, loaded)
                raw = document.get("endpoints")
                if isinstance(raw, list):
                    entries = cast(list[object], raw)
                    for entry in entries:
                        if not isinstance(entry, dict):
                            continue
                        record = cast(JsonObject, entry)
                        entry_provider = record.get("provider")
                        entry_endpoint = record.get("endpoint")
                        if isinstance(entry_provider, str) and isinstance(entry_endpoint, str):
                            pairs.add((entry_provider, entry_endpoint))
            self._allowlist = frozenset(pairs)
        return (provider, endpoint) in self._allowlist

    async def _run_locked(
        self,
        provider: str,
        endpoint: str,
        *,
        body: JsonObject | None = None,
        query_params: JsonObject | None = None,
        path_params: JsonObject | None = None,
    ) -> MonidRun:
        runner = self.runner
        if runner is None:
            raise RuntimeError("Monid command runner is not configured.")

        inspect_args = (
            self.cli,
            "inspect",
            "--json",
            "--provider",
            provider,
            "--endpoint",
            endpoint,
        )
        try:
            inspected = await runner.run(inspect_args, self.inspect_timeout_seconds)
        except CommandTimeoutError as error:
            raise MonidTimeoutError(provider, endpoint, "Monid inspect timed out.") from error
        except OSError as error:
            raise MonidInvocationError(
                provider, endpoint, f"Could not start Monid inspect: {error}"
            ) from error
        try:
            inspect_payload = _parse_command_object(inspected, provider, endpoint, "inspect")
        except MonidInvocationError:
            # The registry inspect command can fail while `monid run` still works.
            # Fall back to the committed discovery allowlist so only endpoints that
            # were live-validated during discovery can ever be executed.
            if not self._allowlist_permits(provider, endpoint):
                raise
        else:
            if (
                inspect_payload.get("provider") != provider
                or inspect_payload.get("endpoint") != endpoint
            ):
                raise MonidSchemaError(
                    provider,
                    endpoint,
                    "Monid inspect returned a different provider or endpoint.",
                )

        run_args: list[str] = [
            self.cli,
            "run",
            "--json",
            "--wait",
            _format_seconds(self.run_timeout_seconds),
            "--provider",
            provider,
            "--endpoint",
            endpoint,
        ]
        if body is not None:
            run_args.extend(("--input", _dump_json(body)))
        if query_params is not None:
            run_args.extend(("--query", _dump_json(query_params)))
        if path_params is not None:
            run_args.extend(("--path", _dump_json(path_params)))

        try:
            command = await runner.run(tuple(run_args), self.run_timeout_seconds + 2)
        except CommandTimeoutError as error:
            raise MonidTimeoutError(
                provider,
                endpoint,
                f"Monid run exceeded {self.run_timeout_seconds:g} seconds.",
            ) from error
        except OSError as error:
            raise MonidInvocationError(
                provider, endpoint, f"Could not start Monid run: {error}"
            ) from error
        payload = _parse_command_object(command, provider, endpoint, "run")
        run = await self._parse_run(payload, provider, endpoint)
        if run.status not in TERMINAL_STATUSES:
            raise MonidSchemaError(
                provider,
                endpoint,
                f"Monid --wait returned nonterminal status {run.status!r}.",
                run=run,
            )
        if run.status != "COMPLETED":
            raise MonidRunError(run)
        if run.provider_http_status is None:
            raise MonidSchemaError(
                provider,
                endpoint,
                "Monid run omitted providerResponse.httpStatus.",
                run=run,
            )
        if not 200 <= run.provider_http_status < 300:
            raise MonidProviderHTTPError(run)
        return run

    async def _parse_run(
        self, payload: JsonObject, requested_provider: str, requested_endpoint: str
    ) -> MonidRun:
        run_id = _required_string(payload, "runId", requested_provider, requested_endpoint)
        raw_status = payload.get("status")
        status = _optional_string(raw_status) or "UNKNOWN"
        raw_provider = payload.get("provider")
        raw_endpoint = payload.get("endpoint")
        provider = _optional_string(raw_provider) or requested_provider
        endpoint = _optional_string(raw_endpoint) or requested_endpoint
        identity_invalid = (
            (raw_provider is not None and _optional_string(raw_provider) is None)
            or (raw_endpoint is not None and _optional_string(raw_endpoint) is None)
            or provider != requested_provider
            or endpoint != requested_endpoint
        )
        provider_response = _optional_object(payload.get("providerResponse"))
        http_status: int | None = None
        http_status_invalid = False
        if provider_response is not None:
            raw_http_status = provider_response.get("httpStatus")
            if isinstance(raw_http_status, int) and not isinstance(raw_http_status, bool):
                http_status = raw_http_status
            elif raw_http_status is not None:
                http_status_invalid = True
        raw_output: JsonValue
        if "output" in payload:
            raw_output = payload["output"]
        elif provider_response is not None and "data" in provider_response:
            raw_output = provider_response["data"]
        else:
            raw_output = None
        cost_error: MonidSchemaError | None = None
        try:
            cost = _parse_cost(payload, provider, endpoint)
        except MonidSchemaError as error:
            cost = None
            cost_error = error
        created_at = _optional_string(payload.get("createdAt"))
        completed_at = _optional_string(payload.get("completedAt"))
        run = MonidRun(
            provider=provider,
            endpoint=endpoint,
            run_id=run_id,
            status=status,
            output=raw_output,
            provider_http_status=http_status,
            cost=cost,
            created_at=created_at,
            completed_at=completed_at,
            retrieved_at=None,
        )
        metadata_run = replace(run, retrieved_at=_utc_now())
        if not isinstance(raw_status, str) or not raw_status:
            raise MonidSchemaError(
                requested_provider,
                requested_endpoint,
                "Monid run omitted status.",
                run=metadata_run,
            )
        if identity_invalid:
            raise MonidSchemaError(
                requested_provider,
                requested_endpoint,
                "Monid run returned a different or invalid provider or endpoint.",
                run=metadata_run,
            )
        if http_status_invalid:
            raise MonidSchemaError(
                provider,
                endpoint,
                "providerResponse.httpStatus must be an integer.",
                run=metadata_run,
            )
        if cost_error is not None:
            raise MonidSchemaError(
                provider, endpoint, str(cost_error), run=metadata_run
            ) from cost_error
        if cost is None and status == "COMPLETED":
            raise MonidSchemaError(
                provider,
                endpoint,
                "Monid run omitted measured billing cost.",
                run=metadata_run,
            )
        if created_at is None:
            raise MonidSchemaError(
                provider, endpoint, "Monid run omitted createdAt.", run=metadata_run
            )
        if status in TERMINAL_STATUSES and completed_at is None:
            raise MonidSchemaError(
                provider,
                endpoint,
                "Terminal Monid run omitted completedAt.",
                run=metadata_run,
            )
        try:
            output = await self._hydrate_artifacts(raw_output, provider, endpoint)
        except MonidError as error:
            error.run = replace(run, retrieved_at=_utc_now())
            raise
        return replace(run, output=output, retrieved_at=_utc_now())

    async def _hydrate_artifacts(
        self, value: JsonValue, provider: str, endpoint: str, depth: int = 0
    ) -> JsonValue:
        if depth > 8:
            raise MonidSchemaError(provider, endpoint, "Monid artifact nesting is too deep.")
        if isinstance(value, dict):
            link = value.get("download_link")
            content_type = value.get("content_type")
            if isinstance(link, str):
                if content_type != "application/json":
                    raise MonidSchemaError(
                        provider, endpoint, "Monid artifact is not application/json."
                    )
                fetcher = self.artifact_fetcher
                if fetcher is None:
                    raise RuntimeError("Monid artifact fetcher is not configured.")
                try:
                    return await asyncio.wait_for(
                        fetcher.fetch_json(link, self.artifact_timeout_seconds),
                        timeout=self.artifact_timeout_seconds,
                    )
                except (TimeoutError, httpx.TimeoutException) as error:
                    raise MonidTimeoutError(
                        provider, endpoint, "Monid artifact download timed out."
                    ) from error
                except (httpx.HTTPError, ValueError) as error:
                    raise MonidSchemaError(
                        provider, endpoint, f"Monid artifact fetch failed: {error}"
                    ) from error
            hydrated: JsonObject = {}
            for key, child in value.items():
                hydrated[key] = await self._hydrate_artifacts(child, provider, endpoint, depth + 1)
            return hydrated
        if isinstance(value, list):
            return [
                await self._hydrate_artifacts(child, provider, endpoint, depth + 1)
                for child in value
            ]
        return value


async def _terminate_process_group(process: asyncio.subprocess.Process) -> None:
    if process.returncode is not None and os.name != "posix":
        return
    group_id = process.pid
    try:
        if os.name == "posix":
            os.killpg(group_id, signal.SIGTERM)
        else:
            process.terminate()
    except ProcessLookupError:
        return
    if process.returncode is None:
        with suppress(TimeoutError):
            await asyncio.wait_for(process.wait(), timeout=0.5)
    try:
        if os.name == "posix":
            os.killpg(group_id, signal.SIGKILL)
        elif process.returncode is None:
            process.kill()
    except ProcessLookupError:
        pass
    if process.returncode is None:
        await process.wait()


def _utc_now() -> str:
    return datetime.now(UTC).isoformat().replace("+00:00", "Z")


def _format_seconds(value: float) -> str:
    return str(int(value)) if value.is_integer() else f"{value:g}"


def _dump_json(value: JsonObject) -> str:
    return json.dumps(value, separators=(",", ":"), sort_keys=True)


def _parse_command_object(
    command: CommandResult, provider: str, endpoint: str, operation: str
) -> JsonObject:
    if command.exit_code != 0:
        detail = command.stderr.strip() or command.stdout.strip() or "unknown CLI error"
        raise MonidInvocationError(provider, endpoint, f"Monid {operation} failed: {detail}")
    try:
        value = _parse_json_value(command.stdout, operation)
    except ValueError as error:
        raise MonidSchemaError(provider, endpoint, str(error)) from error
    if not isinstance(value, dict):
        raise MonidSchemaError(
            provider, endpoint, f"Monid {operation} output must be a JSON object."
        )
    return value


def _parse_json_value(text: str, source: str) -> JsonValue:
    try:
        raw = cast(object, json.loads(text))
    except json.JSONDecodeError as error:
        raise ValueError(f"Invalid JSON from {source}: {error.msg}.") from error
    return _validate_json(raw, source)


def _validate_json(value: object, source: str) -> JsonValue:
    if isinstance(value, float) and not math.isfinite(value):
        raise ValueError(f"JSON from {source} contains a non-finite number.")
    if value is None or isinstance(value, str | int | float | bool):
        return value
    if isinstance(value, list):
        items = cast(list[object], value)
        return [_validate_json(item, source) for item in items]
    if isinstance(value, dict):
        mapping = cast(dict[object, object], value)
        result: JsonObject = {}
        for key, item in mapping.items():
            if not isinstance(key, str):
                raise ValueError(f"JSON object from {source} contains a non-string key.")
            result[key] = _validate_json(item, source)
        return result
    raise ValueError(f"Unsupported JSON value from {source}: {type(value).__name__}.")


def _required_string(payload: JsonObject, key: str, provider: str, endpoint: str) -> str:
    value = payload.get(key)
    if not isinstance(value, str) or not value:
        raise MonidSchemaError(provider, endpoint, f"Monid run omitted {key}.")
    return value


def _optional_string(value: JsonValue | None) -> str | None:
    return value if isinstance(value, str) and value else None


def _optional_object(value: JsonValue | None) -> JsonObject | None:
    return value if isinstance(value, dict) else None


def _parse_cost(payload: JsonObject, provider: str, endpoint: str) -> Money | None:
    direct = _optional_object(payload.get("cost"))
    amount = direct
    if amount is None:
        billing = _optional_object(payload.get("billing"))
        if billing is not None:
            amount = _optional_object(billing.get("reportedCost"))
    if amount is None:
        return None
    raw_value = amount.get("value")
    if (
        isinstance(raw_value, bool)
        or not isinstance(raw_value, int | float)
        or not math.isfinite(raw_value)
    ):
        raise MonidSchemaError(
            provider, endpoint, "Measured cost value must be finite and numeric."
        )
    currency = _optional_string(amount.get("currency")) or "USD"
    unit = _optional_string(amount.get("unit")) or "DOLLAR"
    factors = {
        "DOLLAR": Decimal(1),
        "USD": Decimal(1),
        "CENT": Decimal("0.01"),
        "MILLI_DOLLAR": Decimal("0.001"),
        "MICRO_DOLLAR": Decimal("0.000001"),
    }
    factor = factors.get(unit)
    if factor is None:
        raise MonidSchemaError(provider, endpoint, f"Unknown measured cost unit {unit!r}.")
    return Money(value=Decimal(str(raw_value)) * factor, currency=currency)
