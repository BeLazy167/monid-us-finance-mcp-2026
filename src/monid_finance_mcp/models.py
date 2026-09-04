from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from decimal import Decimal
from typing import TypedDict

type JsonScalar = str | int | float | bool | None
type JsonValue = JsonScalar | Sequence[JsonValue] | Mapping[str, JsonValue]
type JsonObject = dict[str, JsonValue]


class MoneyDict(TypedDict):
    value: float
    currency: str


class TotalCostDict(TypedDict):
    value: float
    currency: str
    complete: bool


class ProvenanceDict(TypedDict):
    provider: str
    endpoint: str
    run_id: str
    lifecycle_status: str
    provider_http_status: int | None
    measured_cost: MoneyDict | None
    created_at: str | None
    completed_at: str | None
    retrieved_at: str
    as_of: str | None
    currency: str | None
    units: str
    data_quality: str


class PartialErrorDict(TypedDict):
    code: str
    message: str
    provider: str | None
    endpoint: str | None
    run_id: str | None
    provider_http_status: int | None


class EnvelopeDict(TypedDict):
    data: JsonObject
    provenance: list[ProvenanceDict]
    total_cost: TotalCostDict
    warnings: list[str]
    partial_errors: list[PartialErrorDict]


@dataclass(frozen=True, slots=True)
class Money:
    value: Decimal = Decimal(0)
    currency: str = "USD"

    def to_dict(self) -> MoneyDict:
        return {"value": float(self.value), "currency": self.currency}


@dataclass(frozen=True, slots=True)
class Provenance:
    provider: str
    endpoint: str
    run_id: str
    lifecycle_status: str
    provider_http_status: int | None
    measured_cost: Money | None
    created_at: str | None
    completed_at: str | None
    retrieved_at: str
    as_of: str | None = None
    currency: str | None = None
    units: str = "provider-reported"
    data_quality: str = "provider-reported"

    def to_dict(self) -> ProvenanceDict:
        return {
            "provider": self.provider,
            "endpoint": self.endpoint,
            "run_id": self.run_id,
            "lifecycle_status": self.lifecycle_status,
            "provider_http_status": self.provider_http_status,
            "measured_cost": (
                self.measured_cost.to_dict() if self.measured_cost is not None else None
            ),
            "created_at": self.created_at,
            "completed_at": self.completed_at,
            "retrieved_at": self.retrieved_at,
            "as_of": self.as_of,
            "currency": self.currency,
            "units": self.units,
            "data_quality": self.data_quality,
        }


@dataclass(frozen=True, slots=True)
class PartialError:
    code: str
    message: str
    provider: str | None = None
    endpoint: str | None = None
    run_id: str | None = None
    provider_http_status: int | None = None

    def to_dict(self) -> PartialErrorDict:
        return {
            "code": self.code,
            "message": self.message,
            "provider": self.provider,
            "endpoint": self.endpoint,
            "run_id": self.run_id,
            "provider_http_status": self.provider_http_status,
        }


def _provenance_list() -> list[Provenance]:
    return []


def _string_list() -> list[str]:
    return []


def _error_list() -> list[PartialError]:
    return []


@dataclass(slots=True)
class Envelope:
    data: JsonObject
    provenance: list[Provenance] = field(default_factory=_provenance_list)
    warnings: list[str] = field(default_factory=_string_list)
    partial_errors: list[PartialError] = field(default_factory=_error_list)
    _total_cost: Decimal = Decimal(0)
    _total_currency: str | None = None
    _cost_complete: bool = True

    def add_provenance(self, item: Provenance) -> None:
        self.provenance.append(item)
        cost = item.measured_cost
        if cost is None:
            self._cost_complete = False
            return
        if self._total_currency is None:
            self._total_currency = cost.currency
        elif cost.currency != self._total_currency:
            self._cost_complete = False
            self.add_error(
                PartialError(
                    code="cost_currency_mismatch",
                    message=(
                        f"Cannot add {cost.currency} cost to "
                        f"{self._total_currency} total without an exchange rate."
                    ),
                    provider=item.provider,
                    endpoint=item.endpoint,
                    run_id=item.run_id,
                    provider_http_status=item.provider_http_status,
                )
            )
            return
        self._total_cost += cost.value

    def add_error(self, error: PartialError) -> None:
        self.partial_errors.append(error)

    def mark_cost_incomplete(self) -> None:
        self._cost_complete = False

    def to_dict(self) -> EnvelopeDict:
        return {
            "data": self.data,
            "provenance": [item.to_dict() for item in self.provenance],
            "total_cost": {
                "value": float(self._total_cost),
                "currency": self._total_currency or "USD",
                "complete": self._cost_complete,
            },
            "warnings": self.warnings,
            "partial_errors": [item.to_dict() for item in self.partial_errors],
        }
