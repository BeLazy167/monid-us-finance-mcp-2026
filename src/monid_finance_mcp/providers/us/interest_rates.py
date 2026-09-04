"""Financial Datasets current policy interest rates from central-bank pages.

Each bank page is fetched with Context.dev /web/scrape/markdown and the
current policy rate is parsed with a strict per-bank regular expression.
A bank whose page cannot be parsed is omitted; rates and dates are never
guessed from anything other than the page text.
"""
from __future__ import annotations

import re
from dataclasses import dataclass

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import SchemaDriftError

SCRAPE_ENDPOINT = "/web/scrape/markdown"

_MONTHS = {
    "january": 1,
    "february": 2,
    "march": 3,
    "april": 4,
    "may": 5,
    "june": 6,
    "july": 7,
    "august": 8,
    "september": 9,
    "october": 10,
    "november": 11,
    "december": 12,
}
_MONTH_ALT = "|".join(_MONTHS)
_FRACTION = re.compile(r"(?P<whole>\d+)-(?P<num>\d)/(?P<den>\d)")
_FED_RANGE = re.compile(
    r"federal\s+funds\s+rate[^\n]{0,220}?"
    r"(?P<a>\d{1,2}(?:\.\d{1,3})?)\s*(?:-|\u2013|\u2014|to)\s*"
    r"(?P<b>\d{1,2}(?:\.\d{1,3})?)\s*percent",
    re.I,
)
_FED_SINGLE = re.compile(
    r"federal\s+funds\s+rate[^\n]{0,220}?"
    r"(?P<a>\d{1,2}(?:\.\d{1,3})?)\s*percent",
    re.I,
)
_ECB_RATE = re.compile(
    r"main\s+refinancing\s+operations[^\n]{0,110}?"
    r"(?P<value>\d{1,2}[.,]\d{1,2})\s*%",
    re.I,
)
_BOE_RATE = re.compile(
    r"bank\s+rate[^\n]{0,130}?(?P<value>\d{1,2}\.\d{1,2})\s*%",
    re.I,
)
_BOJ_RATE = re.compile(
    r"(?:short-term\s+policy\s+interest\s+rate|uncollateralized\s+overnight\s+call\s+rate"
    r"|policy\s+rate)[^\n]{0,180}?(?P<value>\d{1,2}(?:\.\d{1,2})?)\s*percent",
    re.I,
)
_BOJ_RATE_SIGN = re.compile(
    r"(?:short-term\s+policy\s+interest\s+rate|uncollateralized\s+overnight\s+call\s+rate"
    r"|policy\s+rate)[^\n]{0,180}?(?P<value>\d{1,2}(?:\.\d{1,2})?)\s*%",
    re.I,
)
_DATE_US = re.compile(
    rf"(?P<month>{_MONTH_ALT})\s+(?P<day>\d{{1,2}}),?\s+(?P<year>\d{{4}})", re.I
)
_DATE_LONG = re.compile(
    rf"(?P<day>\d{{1,2}})\s+(?P<month>{_MONTH_ALT})\s+(?P<year>\d{{4}})", re.I
)
_DATE_ISO = re.compile(r"(?P<year>20\d{2})-(?P<month>\d{2})-(?P<day>\d{2})")


@dataclass(frozen=True, slots=True)
class BankSpec:
    bank: str
    name: str
    url: str


@dataclass(frozen=True, slots=True)
class BankRate:
    bank: str
    name: str
    rate: float
    date: str | None


BANK_SPECS: tuple[BankSpec, ...] = (
    BankSpec(
        bank="FED",
        name="Federal Reserve",
        url="https://www.federalreserve.gov/monetarypolicy/openmarket.htm",
    ),
    BankSpec(
        bank="ECB",
        name="European Central Bank",
        url=(
            "https://www.ecb.europa.eu/stats/policy_and_exchange_rates/"
            "key_ecb_interest_rates/html/index.en.html"
        ),
    ),
    BankSpec(
        bank="BOE",
        name="Bank of England",
        url="https://www.bankofengland.co.uk/monetary-policy",
    ),
    BankSpec(
        bank="BOJ",
        name="Bank of Japan",
        url="https://www.boj.or.jp/en/mopo/mpr_2026/index.htm",
    ),
)


def scrape_query(url: str, *, timeout_ms: int = 60_000) -> JsonObject:
    """Query params for one Context.dev /web/scrape/markdown call."""
    return {
        "url": url,
        "includeLinks": False,
        "includeImages": False,
        "useMainContentOnly": True,
        "timeoutMS": timeout_ms,
    }


def parse_scrape_markdown(value: JsonValue, *, expected_url: str) -> str:
    """Return the page markdown, validating the Context.dev scrape envelope."""
    payload = value
    if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
        payload = payload["data"]
    if not isinstance(payload, dict):
        raise SchemaDriftError("Context.dev scrape payload is not an object")
    if payload.get("success") is not True:
        raise SchemaDriftError("Context.dev scrape did not report success")
    markdown = payload.get("markdown")
    if not isinstance(markdown, str) or not markdown.strip():
        raise SchemaDriftError("Context.dev scrape returned empty markdown")
    returned_url = payload.get("url")
    if not isinstance(returned_url, str) or returned_url != expected_url:
        raise SchemaDriftError("Context.dev scrape returned a different page URL")
    content_length = payload.get("contentLength")
    if (
        isinstance(content_length, bool)
        or not isinstance(content_length, int)
        or content_length < 0
    ):
        raise SchemaDriftError("Context.dev scrape contentLength must be a non-negative integer")
    return markdown


def parse_policy_rate(markdown: str, *, bank: str) -> BankRate | None:
    """Strictly parse one bank's current policy rate; None when unparseable."""
    spec = _spec(bank)
    if spec is None:
        return None
    if bank == "FED":
        return _fed_rate(markdown, spec)
    if bank == "ECB":
        return _single_rate(markdown, spec, _ECB_RATE)
    if bank == "BOE":
        return _single_rate(markdown, spec, _BOE_RATE)
    if bank == "BOJ":
        found = _single_rate(markdown, spec, _BOJ_RATE)
        if found is not None:
            return found
        return _single_rate(markdown, spec, _BOJ_RATE_SIGN)
    return None


def _fed_rate(markdown: str, spec: BankSpec) -> BankRate | None:
    clean = _expand_fractions(markdown)
    match = _FED_RANGE.search(clean)
    if match is not None:
        lower = float(match.group("a"))
        upper = float(match.group("b"))
        rate = (lower + upper) / 2.0
        position = match.start()
    else:
        single = _FED_SINGLE.search(clean)
        if single is None:
            return None
        rate = float(single.group("a"))
        position = single.start()
    return BankRate(
        bank=spec.bank,
        name=spec.name,
        rate=rate,
        date=_nearby_date(clean, position),
    )


def _single_rate(
    markdown: str, spec: BankSpec, pattern: re.Pattern[str]
) -> BankRate | None:
    match = pattern.search(markdown)
    if match is None:
        return None
    raw = match.group("value").replace(",", ".")
    return BankRate(
        bank=spec.bank,
        name=spec.name,
        rate=float(raw),
        date=_nearby_date(markdown, match.start()),
    )


def _nearby_date(text: str, position: int) -> str | None:
    start = max(0, position - 800)
    window = text[start : position + 400]
    candidates: list[str] = []
    for pattern in (_DATE_US, _DATE_LONG, _DATE_ISO):
        for match in pattern.finditer(window):
            iso = _to_iso(match)
            if iso is not None:
                candidates.append(iso)
    return candidates[-1] if candidates else None


def _to_iso(match: re.Match[str]) -> str | None:
    year = match.groupdict().get("year")
    month = match.groupdict().get("month")
    day = match.groupdict().get("day")
    if year is None or month is None or day is None:
        return None
    month_number = int(month) if month.isdigit() else _MONTHS.get(month.lower())
    if month_number is None:
        return None
    parsed_year = int(year)
    parsed_day = int(day)
    if month_number < 1 or month_number > 12:
        return None
    if parsed_day < 1 or parsed_day > 31:
        return None
    return f"{parsed_year:04d}-{month_number:02d}-{parsed_day:02d}"


def _spec(bank: str) -> BankSpec | None:
    for spec in BANK_SPECS:
        if spec.bank == bank:
            return spec
    return None


def _expand_fractions(markdown: str) -> str:
    def replace(match: re.Match[str]) -> str:
        whole = int(match.group("whole"))
        numerator = int(match.group("num"))
        denominator = int(match.group("den"))
        if denominator == 0:
            return match.group(0)
        value = whole + numerator / denominator
        return f"{value:.3f}".rstrip("0").rstrip(".")

    return _FRACTION.sub(replace, markdown)
