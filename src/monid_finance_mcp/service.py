from __future__ import annotations

import asyncio
from dataclasses import dataclass
from datetime import UTC, date, datetime, time
from typing import Literal

from monid_finance_mcp.client import (
    MonidClientProtocol,
    MonidError,
    MonidInvocationError,
    MonidProviderHTTPError,
    MonidRun,
    MonidRunError,
    MonidSchemaError,
    MonidTimeoutError,
)
from monid_finance_mcp.models import (
    Envelope,
    EnvelopeDict,
    JsonObject,
    PartialError,
    Provenance,
)
from monid_finance_mcp.providers.us.filing_items import (
    CATALOG_SCOPE,
    FilingType,
    catalog_payload,
    normalize_accession,
    parse_filing_sections,
    parse_scrape_payload,
    select_filing,
    validate_catalog_filing_type,
    validate_filing_item_request,
    validate_sec_url,
)
from monid_finance_mcp.providers.us.normalize import (
    InputError,
    SchemaDriftError,
    find_company,
    latest_date,
    normalize_filings,
    normalize_news,
    normalize_prices,
    normalize_statement,
    normalize_stock_price,
    normalize_summary,
    payload_as_of,
    validate_date,
    validate_date_range,
    validate_filing_types,
    validate_interval,
    validate_limit,
    validate_period,
    validate_ticker,
)

DEFILLAMA = "defillama"
CONTEXT = "context.dev"
CATALOG_ENDPOINT = "/equities/v1/companies-list"
SUMMARY_ENDPOINT = "/equities/v1/summary"
STATEMENTS_ENDPOINT = "/equities/v1/statements"
FILINGS_ENDPOINT = "/equities/v1/filings"
OHLCV_ENDPOINT = "/equities/v1/ohlcv"
NEWS_ENDPOINT = "/news/search"
SCRAPE_MARKDOWN_ENDPOINT = "/web/scrape/markdown"
SCRAPE_TIMEOUT_MS = 30_000


@dataclass(frozen=True, slots=True)
class _Outcome:
    result: MonidRun | None = None
    error: MonidError | None = None


class FinanceService:
    def __init__(self, client: MonidClientProtocol) -> None:
        self._client = client

    async def get_company_facts(self, ticker: str) -> EnvelopeDict:
        data: JsonObject = {"company_facts": None, "market_summary": None}
        try:
            symbol = validate_ticker(ticker)
        except InputError as error:
            return _invalid(data, error)

        catalog_outcome, summary_outcome = await asyncio.gather(
            self._call(DEFILLAMA, CATALOG_ENDPOINT),
            self._call(
                DEFILLAMA,
                SUMMARY_ENDPOINT,
                query_params={"ticker": symbol, "country": "US"},
            ),
        )
        envelope = Envelope(data)

        catalog = self._take_result(
            envelope,
            catalog_outcome,
            units="company records",
            data_quality="DefiLlama beta company catalog",
        )
        if catalog is not None:
            try:
                company = find_company(catalog.output, symbol)
            except SchemaDriftError as error:
                _add_schema_error(envelope, catalog, error)
            else:
                data["company_facts"] = company
                if company is None:
                    envelope.warnings.append(
                        f"DefiLlama company catalog has no US record for {symbol}."
                    )
                else:
                    unsupported = [
                        field
                        for field in ("exchange", "sector", "industry", "employee_count")
                        if company[field] is None
                    ]
                    if unsupported:
                        envelope.warnings.append(
                            "DefiLlama company catalog does not report: "
                            + ", ".join(unsupported)
                            + "."
                        )

        summary = self._take_result(
            envelope,
            summary_outcome,
            units="provider-reported market fields",
            data_quality="indicative; real-time licensing not verified",
        )
        if summary is not None:
            try:
                market_summary = normalize_summary(summary.output, symbol)
            except SchemaDriftError as error:
                _add_schema_error(envelope, summary, error)
            else:
                data["market_summary"] = market_summary
                _update_last_provenance(
                    envelope,
                    as_of=payload_as_of(market_summary),
                    currency=_currency(market_summary),
                )
        if data["company_facts"] is None and data["market_summary"] is None:
            envelope.warnings.append(f"No company facts were returned for {symbol}.")
        return envelope.to_dict()

    async def get_income_statement(
        self,
        ticker: str,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
    ) -> EnvelopeDict:
        return await self._get_statement(
            ticker=ticker,
            statement="income",
            output_key="income_statements",
            period=period,
            limit=limit,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
        )

    async def get_balance_sheet(
        self,
        ticker: str,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
    ) -> EnvelopeDict:
        return await self._get_statement(
            ticker=ticker,
            statement="balance",
            output_key="balance_sheets",
            period=period,
            limit=limit,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
        )

    async def get_cash_flow_statement(
        self,
        ticker: str,
        period: str = "annual",
        limit: int = 4,
        report_period: str | None = None,
        report_period_gte: str | None = None,
        report_period_lte: str | None = None,
    ) -> EnvelopeDict:
        return await self._get_statement(
            ticker=ticker,
            statement="cash",
            output_key="cash_flow_statements",
            period=period,
            limit=limit,
            report_period=report_period,
            report_period_gte=report_period_gte,
            report_period_lte=report_period_lte,
        )

    async def get_financial_metrics_snapshot(self, ticker: str) -> EnvelopeDict:
        data: JsonObject = {"financial_metrics_snapshot": None}
        try:
            symbol = validate_ticker(ticker)
        except InputError as error:
            return _invalid(data, error)
        outcome = await self._call(
            DEFILLAMA,
            SUMMARY_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        envelope = Envelope(data)
        result = self._take_result(
            envelope,
            outcome,
            units="provider-reported financial and market metrics",
            data_quality="indicative; real-time licensing not verified",
        )
        if result is not None:
            try:
                snapshot = normalize_summary(result.output, symbol)
            except SchemaDriftError as error:
                _add_schema_error(envelope, result, error)
            else:
                data["financial_metrics_snapshot"] = snapshot
                _update_last_provenance(
                    envelope,
                    as_of=payload_as_of(snapshot),
                    currency=_currency(snapshot),
                )
        if data["financial_metrics_snapshot"] is None and not envelope.partial_errors:
            envelope.warnings.append(f"DefiLlama returned no metrics for {symbol}.")
        return envelope.to_dict()

    async def get_filings(
        self,
        ticker: str,
        filing_type: list[str] | None = None,
        limit: int = 10,
        filing_date_gte: str | None = None,
        filing_date_lte: str | None = None,
    ) -> EnvelopeDict:
        data: JsonObject = {"filings": []}
        try:
            symbol = validate_ticker(ticker)
            types = validate_filing_types(filing_type)
            maximum = validate_limit(limit, maximum=100)
            start, end = validate_date_range(
                filing_date_gte, filing_date_lte, "filing_date_gte", "filing_date_lte"
            )
        except InputError as error:
            return _invalid(data, error)
        outcome = await self._call(
            DEFILLAMA,
            FILINGS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        envelope = Envelope(data)
        result = self._take_result(
            envelope,
            outcome,
            units="filing records",
            data_quality="DefiLlama beta index with direct SEC document URLs",
        )
        if result is not None:
            try:
                filings = normalize_filings(
                    result.output,
                    filing_types=types,
                    limit=maximum,
                    filing_date_gte=start,
                    filing_date_lte=end,
                )
            except SchemaDriftError as error:
                _add_schema_error(envelope, result, error)
            else:
                data["filings"] = filings
                _update_last_provenance(
                    envelope,
                    as_of=latest_date(filings, ("filingDate", "filing_date", "date")),
                )
                if not filings:
                    envelope.warnings.append(f"DefiLlama returned no filings for {symbol}.")
        return envelope.to_dict()

    async def get_filing_items(
        self,
        ticker: str,
        filing_type: FilingType,
        year: int,
        quarter: int | None = None,
        item: str | None = None,
        accession_number: str | None = None,
        include_exhibits: bool = False,
    ) -> EnvelopeDict:
        data: JsonObject = {
            "ticker": ticker,
            "filing_type": filing_type,
            "year": year,
            "quarter": quarter,
            "requested_item": item,
            "include_exhibits": include_exhibits,
            "filing": None,
            "sections": [],
            "scrape": None,
        }
        try:
            symbol, normalized_type, normalized_year, normalized_quarter, selected_item = (
                validate_filing_item_request(ticker, filing_type, year, quarter, item)
            )
            normalized_accession = normalize_accession(accession_number)
        except InputError as error:
            return _invalid(data, error)

        data.update(
            {
                "ticker": symbol,
                "filing_type": normalized_type,
                "year": normalized_year,
                "quarter": normalized_quarter,
                "requested_item": selected_item.name if selected_item is not None else None,
                "accession_number": normalized_accession,
            }
        )
        if include_exhibits:
            return _local_error(
                data,
                code="capability_unavailable",
                message=(
                    "include_exhibits is not supported because the validated route does not "
                    "safely identify and fetch filing exhibits"
                ),
            )

        filing_outcome = await self._call(
            DEFILLAMA,
            FILINGS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        envelope = Envelope(data)
        filing_run = self._take_result(
            envelope,
            filing_outcome,
            units="filing records",
            data_quality="DefiLlama beta filing index used only for deterministic selection",
        )
        if filing_run is None:
            return envelope.to_dict()
        try:
            selection = select_filing(
                filing_run.output,
                filing_type=normalized_type,
                year=normalized_year,
                quarter=normalized_quarter,
                accession_number=normalized_accession,
            )
        except SchemaDriftError as error:
            _add_schema_error(envelope, filing_run, error)
            return envelope.to_dict()
        if selection.filing is None:
            envelope.add_error(
                PartialError(
                    code="filing_not_found",
                    message=(
                        f"No {normalized_type} filing matched {symbol}, year {normalized_year}"
                        + (
                            f", quarter {normalized_quarter}"
                            if normalized_quarter is not None
                            else ""
                        )
                        + (
                            f", accession {normalized_accession}"
                            if normalized_accession is not None
                            else ""
                        )
                        + "."
                    ),
                    provider=filing_run.provider,
                    endpoint=filing_run.endpoint,
                    run_id=filing_run.run_id,
                    provider_http_status=filing_run.provider_http_status,
                )
            )
            return envelope.to_dict()

        filing = selection.filing
        data["filing"] = filing.to_dict()
        _update_last_provenance(envelope, as_of=filing.filing_date)
        if selection.matching_count > 1:
            envelope.warnings.append(
                f"{selection.matching_count} filings matched; selected the latest filing date, "
                "report date, accession, and URL in that order."
            )
        try:
            source_url = validate_sec_url(filing.source_url)
        except ValueError as error:
            envelope.add_error(
                PartialError(
                    code="invalid_source_url",
                    message=str(error),
                    provider=filing_run.provider,
                    endpoint=filing_run.endpoint,
                    run_id=filing_run.run_id,
                    provider_http_status=filing_run.provider_http_status,
                )
            )
            return envelope.to_dict()

        scrape_outcome = await self._call(
            CONTEXT,
            SCRAPE_MARKDOWN_ENDPOINT,
            query_params={
                "url": source_url,
                "includeLinks": False,
                "includeImages": False,
                "useMainContentOnly": True,
                "timeoutMS": SCRAPE_TIMEOUT_MS,
            },
        )
        scrape_run = self._take_result(
            envelope,
            scrape_outcome,
            units="markdown characters",
            data_quality="Context.dev deterministic webpage-to-markdown extraction",
        )
        if scrape_run is None:
            return envelope.to_dict()
        try:
            markdown, scrape_metadata = parse_scrape_payload(scrape_run.output, source_url)
            sections = parse_filing_sections(markdown, normalized_type, selected_item)
        except SchemaDriftError as error:
            _add_schema_error(envelope, scrape_run, error)
            return envelope.to_dict()
        data["scrape"] = scrape_metadata
        data["sections"] = sections
        _update_last_provenance(envelope, as_of=filing.filing_date)
        if not sections:
            missing = selected_item.name if selected_item is not None else "any supported item"
            envelope.add_error(
                PartialError(
                    code="section_not_found",
                    message=f"The filing markdown did not contain body content for {missing}.",
                    provider=scrape_run.provider,
                    endpoint=scrape_run.endpoint,
                    run_id=scrape_run.run_id,
                    provider_http_status=scrape_run.provider_http_status,
                )
            )
        return envelope.to_dict()

    async def list_filing_item_types(self, filing_type: str | None = None) -> EnvelopeDict:
        data: JsonObject = {
            "catalogs": [],
            "catalog_scope": CATALOG_SCOPE,
        }
        try:
            normalized_type = (
                validate_catalog_filing_type(filing_type) if filing_type is not None else None
            )
        except InputError as error:
            return _invalid(data, error)
        return Envelope(catalog_payload(normalized_type)).to_dict()

    async def get_stock_prices(
        self,
        ticker: str,
        start_date: str,
        end_date: str,
        interval: str = "day",
    ) -> EnvelopeDict:
        data: JsonObject = {"ticker": ticker, "interval": interval, "prices": []}
        try:
            symbol = validate_ticker(ticker)
            normalized_interval = validate_interval(interval)
            start, end = validate_date_range(start_date, end_date, "start_date", "end_date")
            if start is None or end is None:
                raise InputError("start_date and end_date are required")
        except InputError as error:
            return _invalid(data, error)
        data["ticker"] = symbol
        data["interval"] = normalized_interval
        outcome = await self._call(
            DEFILLAMA,
            OHLCV_ENDPOINT,
            query_params={"ticker": symbol, "country": "US", "timeframe": "MAX"},
        )
        envelope = Envelope(data)
        result = self._take_result(
            envelope,
            outcome,
            units="provider-reported price and share volume",
            data_quality="delayed/EOD; requested interval aggregation is local",
        )
        if result is not None:
            try:
                prices = normalize_prices(
                    result.output,
                    start_date=start,
                    end_date=end,
                    interval=normalized_interval,
                )
            except (SchemaDriftError, ValueError, OSError) as error:
                _add_schema_error(envelope, result, error)
            else:
                data["prices"] = prices
                _update_last_provenance(envelope, as_of=latest_date(prices), currency="USD")
                if not prices:
                    envelope.warnings.append(
                        f"DefiLlama returned no prices for {symbol} in the requested range."
                    )
        return envelope.to_dict()

    async def get_stock_price(self, ticker: str) -> EnvelopeDict:
        data: JsonObject = {"stock_price": None}
        try:
            symbol = validate_ticker(ticker)
        except InputError as error:
            return _invalid(data, error)
        outcome = await self._call(
            DEFILLAMA,
            SUMMARY_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        envelope = Envelope(data)
        result = self._take_result(
            envelope,
            outcome,
            units="provider-reported market fields",
            data_quality="indicative; real-time licensing not verified",
        )
        if result is not None:
            try:
                snapshot = normalize_stock_price(result.output, symbol)
            except SchemaDriftError as error:
                _add_schema_error(envelope, result, error)
            else:
                data["stock_price"] = snapshot
                _update_last_provenance(
                    envelope,
                    as_of=payload_as_of(snapshot),
                    currency=_currency(snapshot),
                )
        if data["stock_price"] is None and not envelope.partial_errors:
            envelope.warnings.append(f"DefiLlama returned no price for {symbol}.")
        return envelope.to_dict()

    async def get_news(
        self,
        ticker: str | None = None,
        limit: int = 5,
        start_date: str | None = None,
        end_date: str | None = None,
    ) -> EnvelopeDict:
        data: JsonObject = {"news": []}
        try:
            maximum = validate_limit(limit, maximum=10)
            if ticker is None:
                raise InputError("ticker is required for the Context.dev entity-news route")
            symbol = validate_ticker(ticker)
            start, end = validate_date_range(start_date, end_date, "start_date", "end_date")
        except InputError as error:
            return _invalid(data, error)

        body: JsonObject = {
            "searchBy": {
                "type": "entity",
                "entity": {"type": "ticker", "ticker": symbol},
            },
            "sortBy": {"type": "newest"},
            "limit": maximum,
        }
        if start is not None or end is not None:
            date_filter: JsonObject = {}
            if start is not None:
                date_filter["from"] = _epoch_milliseconds(start, end_of_day=False)
            if end is not None:
                date_filter["to"] = _epoch_milliseconds(end, end_of_day=True)
            body["filterBy"] = {"date": date_filter}

        outcome = await self._call(CONTEXT, NEWS_ENDPOINT, body=body)
        envelope = Envelope(data)
        result = self._take_result(
            envelope,
            outcome,
            units="articles",
            data_quality="entity-matched; secondary matches can be noisy",
        )
        if result is not None:
            try:
                news = normalize_news(
                    result.output,
                    limit=maximum,
                    start_date=start,
                    end_date=end,
                )
            except SchemaDriftError as error:
                _add_schema_error(envelope, result, error)
            else:
                data["news"] = news
                _update_last_provenance(envelope, as_of=latest_date(news))
                if not news:
                    envelope.warnings.append(
                        f"Context.dev returned no entity-matched news for {symbol}."
                    )
        return envelope.to_dict()

    async def _get_statement(
        self,
        *,
        ticker: str,
        statement: Literal["income", "balance", "cash"],
        output_key: str,
        period: str,
        limit: int,
        report_period: str | None,
        report_period_gte: str | None,
        report_period_lte: str | None,
    ) -> EnvelopeDict:
        data: JsonObject = {output_key: []}
        try:
            symbol = validate_ticker(ticker)
            normalized_period = validate_period(period)
            maximum = validate_limit(limit, maximum=100)
            exact = validate_date(report_period, "report_period")
            start, end = validate_date_range(
                report_period_gte,
                report_period_lte,
                "report_period_gte",
                "report_period_lte",
            )
            if exact is not None and (start is not None or end is not None):
                raise InputError(
                    "report_period cannot be combined with report_period_gte or report_period_lte"
                )
        except InputError as error:
            return _invalid(data, error)

        outcome = await self._call(
            DEFILLAMA,
            STATEMENTS_ENDPOINT,
            query_params={"ticker": symbol, "country": "US"},
        )
        envelope = Envelope(data)
        result = self._take_result(
            envelope,
            outcome,
            units="not reported by provider",
            data_quality="DefiLlama beta standardized GAAP statements",
        )
        if result is not None:
            try:
                records = normalize_statement(
                    result.output,
                    statement=statement,
                    period=normalized_period,
                    limit=maximum,
                    report_period=exact,
                    report_period_gte=start,
                    report_period_lte=end,
                )
            except SchemaDriftError as error:
                _add_schema_error(envelope, result, error)
            else:
                data[output_key] = records
                _update_last_provenance(envelope, as_of=latest_date(records))
                if not records:
                    reason = (
                        "DefiLlama does not publish a TTM statement block."
                        if normalized_period == "ttm"
                        else f"DefiLlama returned no {statement} statements for {symbol}."
                    )
                    envelope.warnings.append(reason)
        return envelope.to_dict()

    async def _call(
        self,
        provider: str,
        endpoint: str,
        *,
        body: JsonObject | None = None,
        query_params: JsonObject | None = None,
    ) -> _Outcome:
        try:
            result = await self._client.run(
                provider, endpoint, body=body, query_params=query_params
            )
        except MonidError as error:
            return _Outcome(error=error)
        return _Outcome(result=result)

    def _take_result(
        self,
        envelope: Envelope,
        outcome: _Outcome,
        *,
        units: str,
        data_quality: str,
    ) -> MonidRun | None:
        if outcome.result is not None:
            _add_run(envelope, outcome.result, units=units, data_quality=data_quality)
            return outcome.result
        error = outcome.error
        if error is None:
            raise RuntimeError("Monid outcome has neither a result nor an error.")
        failed_run = error.run
        if failed_run is not None:
            _add_run(envelope, failed_run, units=units, data_quality=data_quality)
        else:
            envelope.mark_cost_incomplete()
        envelope.add_error(_partial_error(error, failed_run))
        return None


def _invalid(data: JsonObject, error: InputError) -> EnvelopeDict:
    return _local_error(data, code="invalid_input", message=str(error))


def _local_error(data: JsonObject, *, code: str, message: str) -> EnvelopeDict:
    envelope = Envelope(data)
    envelope.add_error(PartialError(code=code, message=message))
    return envelope.to_dict()


def _partial_error(error: MonidError, run: MonidRun | None) -> PartialError:
    if isinstance(error, MonidProviderHTTPError):
        code = "provider_http_error"
    elif isinstance(error, MonidTimeoutError):
        code = "timeout"
    elif isinstance(error, MonidSchemaError):
        code = "schema_drift"
    elif isinstance(error, MonidInvocationError):
        code = "monid_cli_error"
    elif isinstance(error, MonidRunError):
        code = "run_failed"
    else:
        code = "monid_error"
    return PartialError(
        code=code,
        message=str(error),
        provider=error.provider,
        endpoint=error.endpoint,
        run_id=run.run_id if run is not None else None,
        provider_http_status=run.provider_http_status if run is not None else None,
    )


def _add_run(envelope: Envelope, run: MonidRun, *, units: str, data_quality: str) -> None:
    envelope.add_provenance(
        Provenance(
            provider=run.provider,
            endpoint=run.endpoint,
            run_id=run.run_id,
            lifecycle_status=run.status,
            provider_http_status=run.provider_http_status,
            measured_cost=run.cost,
            created_at=run.created_at,
            completed_at=run.completed_at,
            retrieved_at=run.retrieved_at or datetime.now(UTC).isoformat().replace("+00:00", "Z"),
            units=units,
            data_quality=data_quality,
        )
    )


def _update_last_provenance(
    envelope: Envelope, *, as_of: str | None = None, currency: str | None = None
) -> None:
    if not envelope.provenance:
        return
    previous = envelope.provenance[-1]
    envelope.provenance[-1] = Provenance(
        provider=previous.provider,
        endpoint=previous.endpoint,
        run_id=previous.run_id,
        lifecycle_status=previous.lifecycle_status,
        provider_http_status=previous.provider_http_status,
        measured_cost=previous.measured_cost,
        created_at=previous.created_at,
        completed_at=previous.completed_at,
        retrieved_at=previous.retrieved_at,
        as_of=as_of,
        currency=currency,
        units=previous.units,
        data_quality=previous.data_quality,
    )


def _add_schema_error(envelope: Envelope, run: MonidRun, error: ValueError | OSError) -> None:
    envelope.add_error(
        PartialError(
            code="schema_drift",
            message=str(error),
            provider=run.provider,
            endpoint=run.endpoint,
            run_id=run.run_id,
            provider_http_status=run.provider_http_status,
        )
    )


def _currency(payload: JsonObject) -> str | None:
    for key in ("currency", "currencyCode", "currency_code"):
        value = payload.get(key)
        if isinstance(value, str) and value:
            return value.upper()
    return None


def _epoch_milliseconds(value: date, *, end_of_day: bool) -> int:
    clock = time.max if end_of_day else time.min
    instant = datetime.combine(value, clock, tzinfo=UTC)
    return int(instant.timestamp() * 1000)
