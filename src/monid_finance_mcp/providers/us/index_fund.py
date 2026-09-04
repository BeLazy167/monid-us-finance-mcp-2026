"""Financial Datasets index fund holdings via public fund fact sheets.

Flow: Context.dev /web/search surfaces holdings documents for a fund
ticker, then /web/scrape/markdown fetches the best HTML page and the
holdings table is parsed from markdown. XLSX-only holdings documents are
not routable (the Monid artifact fetcher downloads JSON only), so the
service answers with an honest error when nothing parseable comes back.
Only values stated on the page are emitted; unsourced fields are omitted.
"""
from __future__ import annotations

import re
from math import isfinite
from urllib.parse import urlparse

from monid_finance_mcp.fd import fund_holding_record
from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import InputError, SchemaDriftError

SEARCH_ENDPOINT = "/web/search"
SCRAPE_ENDPOINT = "/web/scrape/markdown"

KNOWN_ISSUER_DOMAINS: dict[str, tuple[str, ...]] = {
    "SPY": ("ssga.com",),
    "IVV": ("ssga.com",),
    "QQQ": ("invesco.com",),
}
_BLOCKED_EXTENSIONS = (".xlsx", ".xls", ".csv")

_MONTHS = {
    "january": 1, "february": 2, "march": 3, "april": 4, "may": 5, "june": 6,
    "july": 7, "august": 8, "september": 9, "october": 10, "november": 11,
    "december": 12,
}
_MONTH_ALT = "|".join(_MONTHS)
_DATE_US = re.compile(
    rf"(?P<month>{_MONTH_ALT})\s+(?P<day>\d{{1,2}}),?\s+(?P<year>\d{{4}})", re.I
)
_AS_OF = re.compile(
    r"as\s+of\s+(?:the\s+)?(?:quarter\s+)?(?:ended\s+)?"
    r"(?P<month>[A-Za-z]+)\s+(?P<day>\d{1,2}),?\s+(?P<year>\d{4})",
    re.I,
)
_TICKER_CELL = re.compile(r"^[A-Za-z0-9][A-Za-z0-9.-]{0,19}$")
_CUSIP_CELL = re.compile(r"^[A-Za-z0-9]{9}$")
_ISIN_CELL = re.compile(r"^[A-Za-z0-9]{12}$")
_HEADER_ALIASES: dict[str, tuple[str, ...]] = {
    "ticker": ("ticker", "symbol", "symbols", "ticker symbol", "fund ticker"),
    "name": ("name", "company", "security", "security name", "holding", "description",
             "fund name", "issuer"),
    "cusip": ("cusip", "cusip number"),
    "isin": ("isin",),
    "weight": ("weight", "weight (%)", "% weight", "portfolio weight", "fund weight",
               "percentage of fund", "% of fund", "net assets (%)", "index weight"),
    "market_value": ("market value", "market value ($)", "value", "market value (usd)",
                     "net assets", "amount", "market value usd"),
    "shares": ("shares", "shares held", "quantity", "share count", "number of shares"),
}


def index_fund_search_request(ticker: str, *, include_domains: bool = True) -> JsonObject:
    """Build the /web/search body that surfaces a fund's holdings documents."""
    body: JsonObject = {
        "query": f"{ticker} ETF holdings xlsx",
        "numResults": 10,
        "markdownOptions": {"enabled": False},
        "timeoutMS": 60_000,
    }
    domains = KNOWN_ISSUER_DOMAINS.get(ticker)
    if include_domains and domains:
        body["includeDomains"] = list(domains)
    return body


def search_rows(value: JsonValue) -> list[JsonObject]:
    """Collect the URL-carrying result objects from a /web/search payload."""
    current = value
    for _ in range(5):
        if isinstance(current, list):
            rows: list[JsonObject] = []
            for index, item in enumerate(current):
                if not isinstance(item, dict):
                    raise SchemaDriftError(f"Web search result[{index}] must be an object")
                rows.append(item)
            return rows
        if isinstance(current, dict):
            child: JsonValue | None = None
            for key in ("results", "web_results", "search_results", "data", "items"):
                candidate = current.get(key)
                if isinstance(candidate, list | dict):
                    child = candidate
                    break
            if child is None:
                break
            current = child
            continue
        break
    raise SchemaDriftError("Web search payload omitted the result list")


def pick_holdings_candidates(value: JsonValue, *, ticker: str) -> list[JsonObject]:
    """Order candidate holdings documents: issuer domain first, then HTML pages."""
    preferred = KNOWN_ISSUER_DOMAINS.get(ticker) or ()

    def score(row: JsonObject, url: str) -> int:
        host = (urlparse(url).hostname or "").lower()
        total = 0
        if any(host.endswith(domain) for domain in preferred):
            total += 3
        path = urlparse(url).path.lower()
        if not path.endswith(_BLOCKED_EXTENSIONS):
            total += 1
        title = row.get("title")
        if isinstance(title, str) and "holding" in title.lower():
            total += 1
        return total

    candidates: list[tuple[int, int, JsonObject]] = []
    for index, row in enumerate(search_rows(value)):
        raw_url = row.get("url") or row.get("link")
        if not isinstance(raw_url, str) or not raw_url:
            continue
        if not raw_url.startswith("https://"):
            continue
        title = row.get("title")
        candidates.append(
            (
                score(row, raw_url),
                index,
                {
                    "url": raw_url,
                    "title": title if isinstance(title, str) and title else None,
                },
            )
        )
    candidates.sort(key=lambda entry: (entry[0], -entry[1]), reverse=True)
    return [candidate for _, _, candidate in candidates]


def scrape_query(url: str, *, timeout_ms: int = 60_000) -> JsonObject:
    return {
        "url": url,
        "includeLinks": False,
        "includeImages": False,
        "useMainContentOnly": True,
        "timeoutMS": timeout_ms,
    }


def parse_scrape_markdown(value: JsonValue, *, expected_url: str) -> str:
    """Return page markdown, validating the Context.dev scrape envelope."""
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
    return markdown


def parse_holdings(markdown: str) -> list[JsonObject]:
    """Parse a fund fact-sheet holdings table from markdown into FD records."""
    rows = _table_rows(markdown)
    if not rows:
        return []
    header_index, column_map = _find_header(rows)
    if header_index is None:
        return []
    if "ticker" not in column_map and "name" not in column_map:
        return []
    records: list[JsonObject] = []
    seen: set[tuple[str, str]] = set()
    for raw_row in rows[header_index + 1 :]:
        cells = _split_row(raw_row)
        if len(cells) < 3:
            continue
        row = {column: cells[index] for column, index in column_map.items() if index < len(cells)}
        ticker = _cell(row.get("ticker"))
        name = _cell(row.get("name"))
        cusip = _cell(row.get("cusip"))
        if ticker is None and name is None and cusip is None:
            continue
        if _is_total_row(row):
            continue
        if _is_decorator_row(row):
            continue
        weight = _number(_cell(row.get("weight")))
        market_value = _number(_cell(row.get("market_value")))
        shares = _number(_cell(row.get("shares")))
        isin = _cell(row.get("isin"))
        dedupe_key = (ticker or "", cusip or "")
        if dedupe_key in seen:
            continue
        seen.add(dedupe_key)
        records.append(
            fund_holding_record(
                ticker=_ticker(ticker),
                name=name,
                cusip=_identifier(cusip, _CUSIP_CELL),
                isin=_identifier(isin, _ISIN_CELL),
                weight=weight,
                market_value=market_value,
                shares=shares,
                asset_class=derive_asset_class(ticker=ticker, name=name, cusip=cusip),
            )
        )
    records.sort(key=_holding_sort_key)
    return records


def _holding_sort_key(record: JsonObject) -> tuple[int, float, str]:
    weight = record.get("weight")
    label = str(record.get("ticker") or record.get("name") or "")
    if isinstance(weight, int | float) and not isinstance(weight, bool):
        return (0, -float(weight), label)
    return (1, 0.0, label)


def parse_as_of(markdown: str) -> str | None:
    """The reporting date the page states ("as of ..."), as YYYY-MM-DD."""
    direct = _AS_OF.search(markdown)
    if direct is not None:
        return _iso(direct.group("month"), direct.group("day"), direct.group("year"))
    for match in _DATE_US.finditer(markdown):
        return _iso(match.group("month"), match.group("day"), match.group("year"))
    return None


def derive_asset_class(*, ticker: str | None, name: str | None, cusip: str | None) -> str:
    """Classify a holding as equity, bond, or other from stated identifiers."""
    haystack = f"{ticker or ''} {name or ''} {cusip or ''}".lower()
    bond_words = (
        "bond", "treasury", "note", "debenture", "agency", "mortgage", "government",
        "money market", "corporate", "municipal",
    )
    if any(word in haystack for word in bond_words):
        return "bond"
    if ticker is not None:
        return "equity"
    return "other"


def validate_asset_class(value: str | None) -> str | None:
    """Normalize the optional asset_class filter (None, equity, or bond)."""
    if value is None:
        return None
    normalized = value.strip().lower()
    if normalized not in {"equity", "bond"}:
        raise InputError("asset_class must be equity or bond")
    return normalized


def _table_rows(markdown: str) -> list[str]:
    return [
        line.strip()
        for line in markdown.splitlines()
        if line.strip().startswith("|") and line.count("|") >= 3
    ]


def _split_row(line: str) -> list[str]:
    parts = line.strip().strip("|").split("|")
    return [part.strip() for part in parts]


def _find_header(rows: list[str]) -> tuple[int | None, dict[str, int]]:
    best_index = -1
    best_mapping: dict[str, int] = {}
    for index, row in enumerate(rows[:8]):
        cells = _split_row(row)
        mapping: dict[str, int] = {}
        for column, header in enumerate(cells):
            key = _header_key(header)
            if key is None:
                continue
            mapping.setdefault(key, column)
        if len(mapping) > len(best_mapping):
            best_index = index
            best_mapping = mapping
    if best_index < 0 or not best_mapping:
        return None, {}
    return best_index, best_mapping


def _header_key(header: str) -> str | None:
    normalized = re.sub(r"[^a-z0-9%]+", " ", header.strip().lower())
    normalized = " ".join(normalized.split())
    for column, aliases in _HEADER_ALIASES.items():
        if normalized in aliases:
            return column
        for alias in aliases:
            if normalized and (normalized == alias or normalized.startswith(alias)):
                return column
    return None


def _is_total_row(row: dict[str, str]) -> bool:
    name = (row.get("name") or "").strip().lower()
    ticker = (row.get("ticker") or "").strip().upper()
    return name == "total" or ticker == "TOTAL"


def _is_decorator_row(row: dict[str, str]) -> bool:
    """Skip markdown table separator rows made only of dashes/equals/colons."""
    cells = [cell for cell in (row.get(key) for key in row) if cell and cell.strip()]
    if not cells:
        return True
    return all(not cell.strip("-=: ") for cell in cells)


def _cell(value: str | None) -> str | None:
    if value is None:
        return None
    cleaned = value.strip()
    return cleaned or None


def _ticker(value: str | None) -> str | None:
    if value is None:
        return None
    cleaned = value.upper()
    return cleaned if _TICKER_CELL.fullmatch(cleaned) else None


def _identifier(value: str | None, pattern: re.Pattern[str]) -> str | None:
    if value is None:
        return None
    cleaned = value.upper()
    return cleaned if pattern.fullmatch(cleaned) else None


def _number(value: str | None) -> float | None:
    if value is None:
        return None
    text = value.strip()
    if not text or text in {"-", "--", "n/a", "N/A", "null", "None"}:
        return None
    negative = text.startswith("(") and text.endswith(")")
    cleaned = text.strip("()$%, ")
    multiplier = 1.0
    last = cleaned[-1:] if cleaned else ""
    if last in {"B", "b"}:
        multiplier = 1_000_000_000.0
        cleaned = cleaned[:-1]
    elif last in {"M", "m"}:
        multiplier = 1_000_000.0
        cleaned = cleaned[:-1]
    elif last in {"K", "k"}:
        multiplier = 1_000.0
        cleaned = cleaned[:-1]
    try:
        parsed = float(cleaned.replace(",", ""))
    except ValueError:
        return None
    result = parsed * multiplier
    if not isfinite(result):
        return None
    return -result if negative else result


def _iso(month: str, day: str, year: str) -> str | None:
    month_number = _MONTHS.get(month.lower())
    if month_number is None:
        return None
    parsed_day = int(day)
    parsed_year = int(year)
    if parsed_day < 1 or parsed_day > 31:
        return None
    return f"{parsed_year:04d}-{month_number:02d}-{parsed_day:02d}"
