from __future__ import annotations

import re
from dataclasses import dataclass
from datetime import date
from typing import Literal
from urllib.parse import urlparse

from monid_finance_mcp.models import JsonObject, JsonValue
from monid_finance_mcp.providers.us.normalize import InputError, SchemaDriftError, validate_ticker

type FilingType = Literal["10-K", "10-Q", "8-K"]

_MIN_FILING_YEAR = 1994
_ACCESSION = re.compile(r"(?<!\d)(\d{10})-?(\d{2})-?(\d{6})(?!\d)")
_PART_HEADING = re.compile(r"^\s*(?:#{1,6}\s*)?(?:\*{1,2})?PART\s+(I{1,3}|IV)\b", re.I)
_ITEM_HEADING = re.compile(
    r"^\s*(?P<markdown>#{1,6}\s*)?(?P<bold>\*{1,2}|__)?"
    r"(?:PART\s+(?P<part>I{1,3}|IV)\s*[-:.]?\s*)?"
    r"ITEM\s+(?P<number>\d{1,2}(?:\.\d{2})?[A-Z]?)"
    r"(?:\s*[.:-]\s*|\s+)(?P<title>.*)$",
    re.I,
)
_MARKDOWN_LINK = re.compile(r"\[([^]]+)]\([^)]+\)")
_MARKUP = re.compile(r"[*_`#]")
_INLINE_XBRL_TAG = re.compile(r"</?ix:", re.I)
_NON_WORD = re.compile(r"[^A-Z0-9]+")
_DOT_LEADER = re.compile(r"(?:\.{2,}|…|\s\d{1,4}\s*$)")


@dataclass(frozen=True, slots=True)
class FilingItem:
    name: str
    number: str
    title: str
    part: str | None = None

    def to_dict(self) -> JsonObject:
        return {
            "name": self.name,
            "number": self.number,
            "title": self.title,
            "part": self.part,
        }


@dataclass(frozen=True, slots=True)
class FilingCatalog:
    filing_type: FilingType
    catalog_version: str
    form_revision: str
    source_url: str
    items: tuple[FilingItem, ...]

    def to_dict(self) -> JsonObject:
        return {
            "filing_type": self.filing_type,
            "catalog_version": self.catalog_version,
            "form_revision": self.form_revision,
            "source": {
                "publisher": "U.S. Securities and Exchange Commission",
                "url": self.source_url,
                "document": f"Form {self.filing_type} instructions",
            },
            "items": [item.to_dict() for item in self.items],
        }


@dataclass(frozen=True, slots=True)
class SelectedFiling:
    filing_date: str
    report_date: str
    form: str
    document_description: str | None
    source_url: str
    accession_number: str

    def to_dict(self) -> JsonObject:
        return {
            "filing_date": self.filing_date,
            "report_date": self.report_date,
            "form": self.form,
            "document_description": self.document_description,
            "source_url": self.source_url,
            "accession_number": self.accession_number,
        }


@dataclass(frozen=True, slots=True)
class FilingSelection:
    filing: SelectedFiling | None
    matching_count: int


@dataclass(frozen=True, slots=True)
class _Heading:
    item: FilingItem
    start: int
    body_start: int
    title_score: int
    looks_like_toc: bool


TEN_K_ITEMS = (
    FilingItem("Item-1", "1", "Business"),
    FilingItem("Item-1A", "1A", "Risk Factors"),
    FilingItem("Item-1B", "1B", "Unresolved Staff Comments"),
    FilingItem("Item-1C", "1C", "Cybersecurity"),
    FilingItem("Item-2", "2", "Properties"),
    FilingItem("Item-3", "3", "Legal Proceedings"),
    FilingItem("Item-4", "4", "Mine Safety Disclosures"),
    FilingItem(
        "Item-5",
        "5",
        "Market for Registrant's Common Equity, Related Stockholder Matters and Issuer "
        "Purchases of Equity Securities",
    ),
    FilingItem("Item-6", "6", "Reserved"),
    FilingItem(
        "Item-7",
        "7",
        "Management's Discussion and Analysis of Financial Condition and Results of Operations",
    ),
    FilingItem("Item-7A", "7A", "Quantitative and Qualitative Disclosures About Market Risk"),
    FilingItem("Item-8", "8", "Financial Statements and Supplementary Data"),
    FilingItem(
        "Item-9",
        "9",
        "Changes in and Disagreements with Accountants on Accounting and Financial Disclosure",
    ),
    FilingItem("Item-9A", "9A", "Controls and Procedures"),
    FilingItem("Item-9B", "9B", "Other Information"),
    FilingItem(
        "Item-9C", "9C", "Disclosure Regarding Foreign Jurisdictions that Prevent Inspections"
    ),
    FilingItem("Item-10", "10", "Directors, Executive Officers and Corporate Governance"),
    FilingItem("Item-11", "11", "Executive Compensation"),
    FilingItem(
        "Item-12",
        "12",
        "Security Ownership of Certain Beneficial Owners and Management and Related "
        "Stockholder Matters",
    ),
    FilingItem(
        "Item-13", "13", "Certain Relationships and Related Transactions, and Director Independence"
    ),
    FilingItem("Item-14", "14", "Principal Accountant Fees and Services"),
    FilingItem("Item-15", "15", "Exhibits and Financial Statement Schedules"),
    FilingItem("Item-16", "16", "Form 10-K Summary"),
)

TEN_Q_ITEMS = (
    FilingItem("Part-I-Item-1", "1", "Financial Statements", "I"),
    FilingItem(
        "Part-I-Item-2",
        "2",
        "Management's Discussion and Analysis of Financial Condition and Results of Operations",
        "I",
    ),
    FilingItem(
        "Part-I-Item-3", "3", "Quantitative and Qualitative Disclosures About Market Risk", "I"
    ),
    FilingItem("Part-I-Item-4", "4", "Controls and Procedures", "I"),
    FilingItem("Part-II-Item-1", "1", "Legal Proceedings", "II"),
    FilingItem("Part-II-Item-1A", "1A", "Risk Factors", "II"),
    FilingItem(
        "Part-II-Item-2",
        "2",
        "Unregistered Sales of Equity Securities and Use of Proceeds",
        "II",
    ),
    FilingItem("Part-II-Item-3", "3", "Defaults Upon Senior Securities", "II"),
    FilingItem("Part-II-Item-4", "4", "Mine Safety Disclosures", "II"),
    FilingItem("Part-II-Item-5", "5", "Other Information", "II"),
    FilingItem("Part-II-Item-6", "6", "Exhibits", "II"),
)

EIGHT_K_ITEMS = (
    FilingItem("Item-1.01", "1.01", "Entry into a Material Definitive Agreement"),
    FilingItem("Item-1.02", "1.02", "Termination of a Material Definitive Agreement"),
    FilingItem("Item-1.03", "1.03", "Bankruptcy or Receivership"),
    FilingItem(
        "Item-1.04", "1.04", "Mine Safety Reporting of Shutdowns and Patterns of Violations"
    ),
    FilingItem("Item-1.05", "1.05", "Material Cybersecurity Incidents"),
    FilingItem("Item-2.01", "2.01", "Completion of Acquisition or Disposition of Assets"),
    FilingItem("Item-2.02", "2.02", "Results of Operations and Financial Condition"),
    FilingItem(
        "Item-2.03",
        "2.03",
        "Creation of a Direct Financial Obligation or an Obligation under an Off-Balance "
        "Sheet Arrangement of a Registrant",
    ),
    FilingItem(
        "Item-2.04",
        "2.04",
        "Triggering Events That Accelerate or Increase a Direct Financial Obligation or an "
        "Obligation under an Off-Balance Sheet Arrangement",
    ),
    FilingItem("Item-2.05", "2.05", "Costs Associated with Exit or Disposal Activities"),
    FilingItem("Item-2.06", "2.06", "Material Impairments"),
    FilingItem(
        "Item-3.01",
        "3.01",
        "Notice of Delisting or Failure to Satisfy a Continued Listing Rule or Standard; "
        "Transfer of Listing",
    ),
    FilingItem("Item-3.02", "3.02", "Unregistered Sales of Equity Securities"),
    FilingItem("Item-3.03", "3.03", "Material Modification to Rights of Security Holders"),
    FilingItem("Item-4.01", "4.01", "Changes in Registrant's Certifying Accountant"),
    FilingItem(
        "Item-4.02",
        "4.02",
        "Non-Reliance on Previously Issued Financial Statements or a Related Audit Report "
        "or Completed Interim Review",
    ),
    FilingItem("Item-5.01", "5.01", "Changes in Control of Registrant"),
    FilingItem(
        "Item-5.02",
        "5.02",
        "Departure of Directors or Certain Officers; Election of Directors; Appointment of "
        "Certain Officers; Compensatory Arrangements of Certain Officers",
    ),
    FilingItem(
        "Item-5.03",
        "5.03",
        "Amendments to Articles of Incorporation or Bylaws; Change in Fiscal Year",
    ),
    FilingItem(
        "Item-5.04",
        "5.04",
        "Temporary Suspension of Trading Under Registrant's Employee Benefit Plans",
    ),
    FilingItem("Item-5.05", "5.05", "Amendments to the Registrant's Code of Ethics, or Waiver"),
    FilingItem("Item-5.06", "5.06", "Change in Shell Company Status"),
    FilingItem("Item-5.07", "5.07", "Submission of Matters to a Vote of Security Holders"),
    FilingItem("Item-5.08", "5.08", "Shareholder Director Nominations"),
    FilingItem("Item-6.01", "6.01", "ABS Informational and Computational Material"),
    FilingItem("Item-6.02", "6.02", "Change of Servicer or Trustee"),
    FilingItem("Item-6.03", "6.03", "Change in Credit Enhancement or Other External Support"),
    FilingItem("Item-6.04", "6.04", "Failure to Make a Required Distribution"),
    FilingItem("Item-6.05", "6.05", "Securities Act Updating Disclosure"),
    FilingItem("Item-7.01", "7.01", "Regulation FD Disclosure"),
    FilingItem("Item-8.01", "8.01", "Other Events"),
    FilingItem("Item-9.01", "9.01", "Financial Statements and Exhibits"),
)

CATALOG_SCOPE = "Static SEC form-instruction catalog; no Monid upstream call or claim."

CATALOGS: dict[FilingType, FilingCatalog] = {
    "10-K": FilingCatalog(
        "10-K",
        "SEC-1673-02-25",
        "SEC 1673 (02-25)",
        "https://www.sec.gov/files/form10-k.pdf",
        TEN_K_ITEMS,
    ),
    "10-Q": FilingCatalog(
        "10-Q",
        "SEC-1296-02-25",
        "SEC 1296 (02-25)",
        "https://www.sec.gov/files/form10-q.pdf",
        TEN_Q_ITEMS,
    ),
    "8-K": FilingCatalog(
        "8-K",
        "SEC-873-02-25",
        "SEC 873 (02-25)",
        "https://www.sec.gov/files/form8-k.pdf",
        EIGHT_K_ITEMS,
    ),
}


def validate_filing_item_request(
    ticker: str,
    filing_type: str,
    year: int,
    quarter: int | None,
    item: str | None,
) -> tuple[str, FilingType, int, int | None, FilingItem | None]:
    symbol = validate_ticker(ticker)
    normalized_type = validate_catalog_filing_type(filing_type)
    current_year = date.today().year
    if isinstance(year, bool) or not _MIN_FILING_YEAR <= year <= current_year + 1:
        raise InputError(f"year must be between {_MIN_FILING_YEAR} and {current_year + 1}")
    if quarter is not None and (isinstance(quarter, bool) or not 1 <= quarter <= 4):
        raise InputError("quarter must be between 1 and 4")
    selected_item = resolve_item(normalized_type, item) if item is not None else None
    return symbol, normalized_type, year, quarter, selected_item


def validate_catalog_filing_type(value: str) -> FilingType:
    normalized = value.strip().upper()
    if normalized not in CATALOGS:
        raise InputError("filing_type must be 10-K, 10-Q, or 8-K")
    return normalized


def normalize_accession(value: str | None) -> str | None:
    if value is None:
        return None
    match = _ACCESSION.fullmatch(value.strip())
    if match is None:
        raise InputError("accession_number must contain 18 digits, with optional SEC dashes")
    return "-".join(match.groups())


def derive_accession(url: str) -> str | None:
    match = _ACCESSION.search(urlparse(url).path)
    return "-".join(match.groups()) if match is not None else None


def validate_sec_url(value: str) -> str:
    try:
        parsed = urlparse(value)
        port = parsed.port
    except ValueError as error:
        raise ValueError("selected filing URL is malformed") from error
    if (
        parsed.scheme.lower() != "https"
        or parsed.hostname not in {"sec.gov", "www.sec.gov"}
        or parsed.username is not None
        or parsed.password is not None
        or port not in {None, 443}
        or not parsed.path.startswith("/Archives/")
    ):
        raise ValueError("selected filing URL must use HTTPS on sec.gov and point under /Archives/")
    if derive_accession(value) is None:
        raise ValueError("selected SEC filing URL does not contain an accession number")
    return value


def resolve_item(filing_type: FilingType, value: str) -> FilingItem:
    catalog = CATALOGS[filing_type]
    key = _alias_key(value)
    matches = [item for item in catalog.items if key in _item_aliases(item)]
    if len(matches) == 1:
        return matches[0]
    if len(matches) > 1:
        names = ", ".join(item.name for item in matches)
        raise InputError(f"item {value!r} is ambiguous for {filing_type}; use one of: {names}")
    allowed = ", ".join(item.name for item in catalog.items)
    raise InputError(f"item must be one of the supported {filing_type} names: {allowed}")


def select_filing(
    value: JsonValue,
    *,
    filing_type: FilingType,
    year: int,
    quarter: int | None,
    accession_number: str | None,
) -> FilingSelection:
    records = _filing_records(value)
    matches: list[tuple[date, date, str, str, SelectedFiling]] = []
    for record in records:
        form = _optional_string(record.get("form"))
        if form is None or form.strip().upper() != filing_type:
            continue
        report_date = _iso_date(record.get("reportDate"))
        filing_date = _iso_date(record.get("filingDate"))
        if report_date is None or filing_date is None or report_date.year != year:
            continue
        if quarter is not None and ((report_date.month - 1) // 3) + 1 != quarter:
            continue
        source_url = _optional_string(record.get("primaryDocumentUrl"))
        if source_url is None:
            continue
        accession = derive_accession(source_url)
        if accession_number is not None and accession != accession_number:
            continue
        if accession is None:
            accession = ""
        description = _optional_string(record.get("documentDescription"))
        selected = SelectedFiling(
            filing_date=filing_date.isoformat(),
            report_date=report_date.isoformat(),
            form=form.strip().upper(),
            document_description=description,
            source_url=source_url,
            accession_number=accession,
        )
        matches.append((filing_date, report_date, accession, source_url, selected))
    matches.sort(key=lambda candidate: candidate[:4], reverse=True)
    if not matches:
        return FilingSelection(None, 0)
    return FilingSelection(matches[0][4], len(matches))


def parse_scrape_payload(value: JsonValue, expected_url: str) -> tuple[str, JsonObject]:
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
    content_length = payload.get("contentLength")
    if (
        isinstance(content_length, bool)
        or not isinstance(content_length, int)
        or content_length < 0
    ):
        raise SchemaDriftError("Context.dev scrape contentLength must be a non-negative integer")
    returned_url = payload.get("url")
    if not isinstance(returned_url, str):
        raise SchemaDriftError("Context.dev scrape omitted its source URL")
    try:
        validate_sec_url(returned_url)
    except ValueError as error:
        raise SchemaDriftError(
            f"Context.dev scrape returned an unsafe source URL: {error}"
        ) from error
    if returned_url != expected_url:
        raise SchemaDriftError("Context.dev scrape returned a different SEC filing URL")
    metadata = payload.get("metadata")
    cache_metadata = payload.get("cache_metadata")
    return markdown, {
        "content_length": content_length,
        "url": returned_url,
        "metadata": metadata,
        "cache_metadata": cache_metadata,
    }


def parse_filing_sections(
    markdown: str,
    filing_type: FilingType,
    requested_item: FilingItem | None,
) -> list[JsonObject]:
    if not markdown.strip():
        raise SchemaDriftError("Context.dev scrape returned empty markdown")
    catalog = CATALOGS[filing_type]
    headings = _find_headings(markdown, catalog)
    wanted = (requested_item,) if requested_item is not None else catalog.items
    sections: list[JsonObject] = []
    for item in wanted:
        candidates = [heading for heading in headings if heading.item == item]
        best = _best_heading(markdown, candidates, headings)
        if best is None:
            continue
        end = _section_end(best, headings, len(markdown))
        content = markdown[best.body_start : end].strip()
        if not content:
            continue
        sections.append(
            {
                "item": item.name,
                "number": item.number,
                "part": item.part,
                "title": item.title,
                "content": content,
            }
        )
    return sections


def catalog_payload(filing_type: FilingType | None) -> JsonObject:
    selected = [CATALOGS[filing_type]] if filing_type is not None else list(CATALOGS.values())
    return {
        "catalogs": [catalog.to_dict() for catalog in selected],
        "catalog_scope": CATALOG_SCOPE,
    }


def _filing_records(value: JsonValue) -> list[JsonObject]:
    current = value
    for _ in range(4):
        if isinstance(current, list):
            records: list[JsonObject] = []
            for index, record in enumerate(current):
                if not isinstance(record, dict):
                    raise SchemaDriftError(f"DefiLlama filing row {index} is not an object")
                records.append(record)
            return records
        if not isinstance(current, dict):
            break
        child = current.get("data")
        if isinstance(child, list | dict):
            current = child
            continue
        child = current.get("filings")
        if isinstance(child, list | dict):
            current = child
            continue
        break
    raise SchemaDriftError("DefiLlama payload omitted filing records")


def _iso_date(value: JsonValue | None) -> date | None:
    if not isinstance(value, str):
        return None
    try:
        return date.fromisoformat(value[:10])
    except ValueError:
        return None


def _optional_string(value: JsonValue | None) -> str | None:
    return value if isinstance(value, str) and value.strip() else None


def _item_aliases(item: FilingItem) -> frozenset[str]:
    aliases = {
        _alias_key(item.name),
        _alias_key(f"Item {item.number}"),
        _alias_key(item.number),
    }
    if item.part is not None:
        numeric_part = "1" if item.part == "I" else "2"
        aliases.update(
            {
                _alias_key(f"Part {item.part} Item {item.number}"),
                _alias_key(f"{item.part} Item {item.number}"),
                _alias_key(f"{item.part}-{item.number}"),
                _alias_key(f"Part-{numeric_part},Item-{item.number}"),
            }
        )
    return frozenset(aliases)


def _alias_key(value: str) -> str:
    return _NON_WORD.sub("", value.upper())


def _find_headings(markdown: str, catalog: FilingCatalog) -> list[_Heading]:
    lookup: dict[tuple[str | None, str], FilingItem] = {
        (item.part, item.number): item for item in catalog.items
    }
    number_counts: dict[str, int] = {}
    for item in catalog.items:
        number_counts[item.number] = number_counts.get(item.number, 0) + 1
    headings: list[_Heading] = []
    current_part: str | None = None
    toc_active = False
    toc_items: set[str] = set()
    toc_parts: set[str] = set()
    offset = 0
    for raw_line in markdown.splitlines(keepends=True):
        line = raw_line.rstrip("\r\n")
        if _INLINE_XBRL_TAG.search(line):
            offset += len(raw_line)
            continue
        clean = _clean_heading_line(line)
        if _alias_key(clean) == "TABLEOFCONTENTS":
            toc_active = True
            toc_items.clear()
            toc_parts.clear()
        part_match = _PART_HEADING.match(clean)
        if part_match is not None:
            matched_part = part_match.group(1)
            if matched_part is not None:
                normalized_part = matched_part.upper()
                current_part = normalized_part
                if toc_active and normalized_part in toc_parts:
                    toc_active = False
                elif toc_active:
                    toc_parts.add(normalized_part)
        item_match = _ITEM_HEADING.match(clean)
        if item_match is not None:
            number = item_match.group("number").upper()
            explicit_part = item_match.group("part")
            part = explicit_part.upper() if explicit_part is not None else current_part
            item = lookup.get((part, number))
            if item is None and number_counts.get(number) == 1:
                item = next(candidate for candidate in catalog.items if candidate.number == number)
            if item is not None:
                if toc_active and item.name in toc_items:
                    toc_active = False
                title = item_match.group("title").strip()
                title_score = _title_score(title, item.title)
                has_heading_markup = bool(item_match.group("markdown") or item_match.group("bold"))
                label_is_upper = _letters_are_upper(clean)
                if title_score > 0 or has_heading_markup or label_is_upper:
                    headings.append(
                        _Heading(
                            item=item,
                            start=offset,
                            body_start=offset + len(raw_line),
                            title_score=title_score,
                            looks_like_toc=toc_active or bool(_DOT_LEADER.search(title)),
                        )
                    )
                    if toc_active:
                        toc_items.add(item.name)
        offset += len(raw_line)
    return headings


def _clean_heading_line(value: str) -> str:
    linked = _MARKDOWN_LINK.sub(r"\1", value)
    return re.sub(r"<[^>]+>", "", linked).strip()


def _letters_are_upper(value: str) -> bool:
    letters = [character for character in value if character.isalpha()]
    return bool(letters) and all(character.isupper() for character in letters)


def _title_score(actual: str, expected: str) -> int:
    actual_words = set(_NON_WORD.sub(" ", _MARKUP.sub("", actual).upper()).split())
    expected_words = set(_NON_WORD.sub(" ", expected.upper()).split())
    if not actual_words:
        return 0
    shared = actual_words & expected_words
    return int(100 * len(shared) / max(1, len(expected_words)))


def _best_heading(
    markdown: str,
    candidates: list[_Heading],
    headings: list[_Heading],
) -> _Heading | None:
    ranked: list[tuple[tuple[int, int, int, int], _Heading]] = []
    for candidate in candidates:
        if candidate.looks_like_toc:
            continue
        end = _section_end(candidate, headings, len(markdown))
        content_length = len(markdown[candidate.body_start : end].strip())
        if content_length == 0:
            continue
        ranked.append(
            (
                (
                    int(content_length >= 80),
                    candidate.start,
                    min(content_length, 200_000),
                    candidate.title_score,
                ),
                candidate,
            )
        )
    if not ranked:
        return None
    return max(ranked, key=lambda entry: entry[0])[1]


def _section_end(
    heading: _Heading,
    headings: list[_Heading],
    document_end: int,
) -> int:
    later = [candidate.start for candidate in headings if candidate.start > heading.start]
    return min(later, default=document_end)
