package service

import (
	"context"
	"math"
	"testing"
)

// --- compound-string field parsing (get_beneficial_ownership) ---

func TestParseSharesAndPercent_WellFormed(t *testing.T) {
	shares, percent := parseSharesAndPercent("1,171,112 16.070%")
	if shares == nil || *shares != 1171112 {
		t.Fatalf("expected shares 1171112, got %v", shares)
	}
	if percent == nil || *percent != 16.070 {
		t.Fatalf("expected percent 16.070, got %v", percent)
	}
}

func TestParseSharesAndPercent_MalformedOmitsRatherThanGuesses(t *testing.T) {
	// Neither half is unambiguously parseable: the whole field is garbage.
	shares, percent := parseSharesAndPercent("N/A")
	if shares != nil || percent != nil {
		t.Fatalf("expected both fields omitted for garbage input, got shares=%v percent=%v", shares, percent)
	}
	// A parseable share count with a non-numeric percent: keep the shares,
	// omit the percent rather than guessing at it.
	shares, percent = parseSharesAndPercent("1,171,112 unknown%")
	if shares == nil || *shares != 1171112 {
		t.Fatalf("expected shares 1171112 salvaged, got %v", shares)
	}
	if percent != nil {
		t.Fatalf("expected percent omitted for unparseable half, got %v", percent)
	}
}

func TestParseDeltaAndPercent_WellFormed(t *testing.T) {
	delta, percent := parseDeltaAndPercent("204,765 (+21.19%)")
	if delta == nil || *delta != 204765 {
		t.Fatalf("expected delta 204765, got %v", delta)
	}
	if percent == nil || *percent != 21.19 {
		t.Fatalf("expected percent 21.19, got %v", percent)
	}
}

func TestParseDeltaAndPercent_MalformedOmitsRatherThanGuesses(t *testing.T) {
	delta, percent := parseDeltaAndPercent("unchanged")
	if delta != nil || percent != nil {
		t.Fatalf("expected both fields omitted for garbage input, got delta=%v percent=%v", delta, percent)
	}
	// A delta with no parenthesized percent: keep the delta, omit the percent.
	delta, percent = parseDeltaAndPercent("204,765")
	if delta == nil || *delta != 204765 {
		t.Fatalf("expected delta 204765 salvaged, got %v", delta)
	}
	if percent != nil {
		t.Fatalf("expected percent omitted with no parenthesized figure, got %v", percent)
	}
}

// --- CIK derivation from a filings lookup ---

func TestCikFromEdgarURL(t *testing.T) {
	cik := cikFromEdgarURL("https://www.sec.gov/Archives/edgar/data/0000320193/000032019325000079/aapl-20251231.htm")
	if cik == nil || *cik != "320193" {
		t.Fatalf("expected CIK 320193, got %v", cik)
	}
	if got := cikFromEdgarURL("https://www.sec.gov/not-edgar/whatever.htm"); got != nil {
		t.Fatalf("expected nil for a non-EDGAR URL, got %v", *got)
	}
	if got := cikFromEdgarURL("https://www.sec.gov/Archives/edgar/data/0000000000/x.htm"); got != nil {
		t.Fatalf("expected nil for the all-zero CIK, got %v", *got)
	}
}

func TestResolveIssuerCIK_HappyPathAndNotFound(t *testing.T) {
	filings := []any{
		map[string]any{
			"filingDate": "2026-02-01", "reportDate": "2025-12-31", "form": "10-K",
			"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/0000320193/000032019325000079/aapl-20251231.htm",
		},
	}
	svc, transport := newTestService(t, map[string]fakeOutcome{
		"defillama /equities/v1/filings": {output: map[string]any{"filings": filings}},
	})
	cc := svc.newCallCtx(context.Background(), "key", "test")
	cik, found, err := cc.resolveIssuerCIK("AAPL")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found || cik != "320193" {
		t.Fatalf("expected CIK 320193 found, got found=%v cik=%q", found, cik)
	}
	if transport.CallCount() != 1 {
		t.Fatalf("expected exactly one filings call, got %d", transport.CallCount())
	}

	svc2, _ := newTestService(t, map[string]fakeOutcome{
		"defillama /equities/v1/filings": {output: map[string]any{"filings": []any{
			map[string]any{"form": "10-K", "primaryDocumentUrl": "https://www.sec.gov/no-cik-here.htm"},
		}}},
	})
	cc2 := svc2.newCallCtx(context.Background(), "key", "test")
	_, found2, err2 := cc2.resolveIssuerCIK("AAPL")
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if found2 {
		t.Fatalf("expected found=false when no filing URL carries a CIK")
	}
}

// --- get_beneficial_owners: distinct filers + name prefix filter ---

// thirteenDItem/thirteenGItem are shaped after the live-verified
// /get_13d_filings and /get_13g_filings item fields (see
// docs/compatibility.md's freshness note): reported_datetime,
// transaction_date, type, company_symbol, company_symbol_url,
// filed_by_symbol, filed_by_symbol_url, shares_owned_owned,
// shares_vs_prev_report, view, view_url.
func thirteenDItem(companySymbol, filedBy, ownedOwned, vsPrev string) map[string]any {
	return map[string]any{
		"reported_datetime":     "2026-03-06 9:15 am",
		"transaction_date":      "2026-03-04",
		"type":                  "13D",
		"company_symbol":        companySymbol,
		"company_symbol_url":    "https://www.secform4.com/#" + companySymbol,
		"filed_by_symbol":       filedBy,
		"filed_by_symbol_url":   "https://www.secform4.com/insider/" + filedBy,
		"shares_owned_owned":    ownedOwned,
		"shares_vs_prev_report": vsPrev,
		"view":                  "View",
		"view_url":              "https://www.secform4.com/filings/1.htm",
	}
}

func thirteenGItem(companySymbol, filedBy, ownedOwned, vsPrev string) map[string]any {
	item := thirteenDItem(companySymbol, filedBy, ownedOwned, vsPrev)
	item["type"] = "13G"
	return item
}

func TestGetBeneficialOwners_DistinctFilersAndNameFilter(t *testing.T) {
	dPayload := map[string]any{"status": "success", "data": map[string]any{"items": []any{
		thirteenDItem("AAPL", "Icahn Carl C", "1,171,112 16.070%", "204,765 (+21.19%)"),
	}}}
	gPayload := map[string]any{"status": "success", "data": map[string]any{"items": []any{
		thirteenGItem("AAPL", "Vanguard Group Inc", "50,000,000 8.500%", "1,000 (+0.01%)"),
		thirteenGItem("MSFT", "Icahn Carl C", "500,000 1.000%", "0 (+0.00%)"), // duplicate filer name
	}}}
	svc, _ := newTestService(t, map[string]fakeOutcome{
		"secform4 /get_13d_filings": {output: dPayload},
		"secform4 /get_13g_filings": {output: gPayload},
	})
	result := mustCall(t, svc, "get_beneficial_owners", map[string]any{})
	records := asRecords(t, result.Value)
	if len(records) != 2 {
		t.Fatalf("expected 2 distinct filers, got %d: %#v", len(records), records)
	}

	filtered := mustCall(t, svc, "get_beneficial_owners", map[string]any{"name": "van"})
	filteredRecords := asRecords(t, filtered.Value)
	if len(filteredRecords) != 1 {
		t.Fatalf("expected 1 filer matching name prefix 'van', got %d", len(filteredRecords))
	}
	row := jsonRoundTrip(t, filteredRecords[0])
	if row["reporting_person_name"] != "Vanguard Group Inc" {
		t.Fatalf("unexpected filer: %#v", row)
	}
}

// --- get_beneficial_ownership: ticker path, compound parsing, FD shape ---

func TestGetBeneficialOwnership_TickerPathAndFDShape(t *testing.T) {
	dPayload := map[string]any{"status": "success", "data": map[string]any{"items": []any{
		thirteenDItem("AAPL", "Icahn Carl C", "1,171,112 16.070%", "204,765 (+21.19%)"),
	}}}
	gPayload := map[string]any{"status": "success", "data": map[string]any{"items": []any{
		thirteenGItem("MSFT", "Vanguard Group Inc", "50,000,000 8.500%", "1,000 (+0.01%)"),
	}}}
	svc, transport := newTestService(t, map[string]fakeOutcome{
		"secform4 /get_13d_filings": {output: dPayload},
		"secform4 /get_13g_filings": {output: gPayload},
	})
	result := mustCall(t, svc, "get_beneficial_ownership", map[string]any{"ticker": "AAPL"})
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 AAPL stake, got %d", len(records))
	}
	row := jsonRoundTrip(t, records[0])
	if row["ticker"] != "AAPL" || row["reporting_person_name"] != "Icahn Carl C" {
		t.Fatalf("unexpected row: %#v", row)
	}
	if row["type"] != "activist" || row["form_type"] != "SCHEDULE 13D" {
		t.Fatalf("expected activist SCHEDULE 13D, got %#v", row)
	}
	if row["filing_date"] != "2026-03-06" || row["event_date"] != "2026-03-04" {
		t.Fatalf("expected sourced filing/event dates, got %#v", row)
	}
	if row["aggregate_amount_beneficially_owned"] != 1171112.0 || row["percent_of_class"] != 16.070 {
		t.Fatalf("expected parsed shares/percent, got %#v", row)
	}
	if row["share_change"] != 204765.0 || row["share_change_percent"] != 21.19 {
		t.Fatalf("expected parsed share change fields, got %#v", row)
	}
	// Fields this feed never carries (issuer_cik, voting powers, ...) must
	// never appear fabricated in the response.
	for _, absent := range []string{"issuer_cik", "sole_voting_power", "purpose_of_transaction", "is_latest"} {
		if _, present := row[absent]; present {
			t.Fatalf("expected %q to be omitted (never fabricated), got %#v", absent, row[absent])
		}
	}
	if transport.CallCount() != 2 {
		t.Fatalf("expected both 13D and 13G calls when type is omitted, got %d", transport.CallCount())
	}

	// history=true is accepted for schema parity but honestly rejected.
	rejected := mustCall(t, svc, "get_beneficial_ownership", map[string]any{"ticker": "AAPL", "history": true})
	body := jsonRoundTrip(t, rejected.Value)
	if body["error"] != "bad_request" {
		t.Fatalf("expected bad_request for history=true, got %#v", body)
	}

	// Exactly one of ticker/filer_cik is required.
	if _, err := svc.Call(context.Background(), "key", "get_beneficial_ownership", map[string]any{}); err == nil {
		t.Fatalf("expected an error when neither ticker nor filer_cik is given")
	}
	if _, err := svc.Call(context.Background(), "key", "get_beneficial_ownership",
		map[string]any{"ticker": "AAPL", "filer_cik": "320193"}); err == nil {
		t.Fatalf("expected an error when both ticker and filer_cik are given")
	}
}

func TestGetBeneficialOwnership_TypeFilterCallsOnlyThatFeed(t *testing.T) {
	dPayload := map[string]any{"status": "success", "data": map[string]any{"items": []any{
		thirteenDItem("AAPL", "Icahn Carl C", "1,171,112 16.070%", "204,765 (+21.19%)"),
	}}}
	svc, transport := newTestService(t, map[string]fakeOutcome{
		"secform4 /get_13d_filings": {output: dPayload},
	})
	result := mustCall(t, svc, "get_beneficial_ownership", map[string]any{"ticker": "AAPL", "type": "activist"})
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if transport.CallCount() != 1 {
		t.Fatalf("expected exactly one call (13D only) when type=activist, got %d", transport.CallCount())
	}
}

// --- get_insider_ownership: CIK derivation, aggregation, not-found path ---

func TestGetInsiderOwnership_AggregatesMostRecentHoldingAndNotFound(t *testing.T) {
	filings := []any{
		map[string]any{
			"form":               "10-K",
			"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/0000320193/000032019325000079/aapl-20251231.htm",
		},
	}
	insiderPayload := map[string]any{"status": "success", "data": map[string]any{"results": []any{
		map[string]any{
			"insider_relationship": "Cook Timothy D", "company": "Apple Inc.",
			"reported_datetime": "2026-01-10 9:00 am", "transaction_date": "2026-01-08",
			"shares_owned": "3,000,000 (Direct)",
		},
		map[string]any{
			// Same insider, an OLDER row: must be superseded by the newer one above.
			"insider_relationship": "Cook Timothy D", "company": "Apple Inc.",
			"reported_datetime": "2025-06-01 9:00 am", "transaction_date": "2025-05-30",
			"shares_owned": "2,500,000 (Direct)",
		},
	}}}
	svc, transport := newTestService(t, map[string]fakeOutcome{
		"defillama /equities/v1/filings":        {output: map[string]any{"filings": filings}},
		"secform4 /get_company_insider_trading": {output: insiderPayload},
	})
	result := mustCall(t, svc, "get_insider_ownership", map[string]any{"ticker": "AAPL"})
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 aggregated insider row, got %d: %#v", len(records), records)
	}
	row := jsonRoundTrip(t, records[0])
	if row["name"] != "Cook Timothy D" || row["shares_owned"] != 3000000.0 {
		t.Fatalf("expected the most recent post-transaction holding kept, got %#v", row)
	}
	if row["as_of_date"] != "2026-01-08" || row["filing_date"] != "2026-01-10" {
		t.Fatalf("unexpected dates: %#v", row)
	}
	if row["direct_or_indirect"] != "Direct" {
		t.Fatalf("expected direct_or_indirect Direct, got %#v", row)
	}
	if _, present := row["cik"]; present {
		t.Fatalf("CIK is plumbing for the upstream call, never a response field; got %#v", row["cik"])
	}
	// The insider-trading call must have carried the CIK derived from the
	// filings lookup, not the ticker.
	found := false
	for _, call := range transport.Calls() {
		if call.Provider == "secform4" && call.Endpoint == "/get_company_insider_trading" {
			if call.QueryParams["cik"] == "320193" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected the insider-trading call to carry cik=320193, got %#v", transport.Calls())
	}

	// form_type is accepted for schema parity but honestly rejected: this
	// feed carries no Form 3/5 classification to filter by.
	rejected := mustCall(t, svc, "get_insider_ownership", map[string]any{"ticker": "AAPL", "form_type": "3"})
	body := jsonRoundTrip(t, rejected.Value)
	if body["error"] != "bad_request" {
		t.Fatalf("expected bad_request for form_type, got %#v", body)
	}

	// Not-found path: a ticker whose filings carry no derivable CIK.
	svc2, _ := newTestService(t, map[string]fakeOutcome{
		"defillama /equities/v1/filings": {output: map[string]any{"filings": []any{
			map[string]any{"form": "10-K", "primaryDocumentUrl": "https://www.sec.gov/no-cik.htm"},
		}}},
	})
	notFound := mustCall(t, svc2, "get_insider_ownership", map[string]any{"ticker": "MSFT"})
	notFoundBody := jsonRoundTrip(t, notFound.Value)
	if notFoundBody["error"] != "not_found" {
		t.Fatalf("expected not_found when no CIK resolves, got %#v", notFoundBody)
	}
}

// --- get_institutional_investors: directory + name filter ---

func TestGetInstitutionalInvestors_DirectoryAndNameFilter(t *testing.T) {
	payload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"rows": []any{
				map[string]any{"filer_name": "Vanguard Group Inc", "shares": 100_000.0},
				map[string]any{"filer_name": "Berkshire Hathaway Inc", "filer_cik": "0001067983", "shares": 50_000.0},
				map[string]any{"filer_name": "Vanguard Group Inc", "shares": 99.0}, // duplicate filer
			},
		},
	}
	svc, _ := newTestService(t, map[string]fakeOutcome{
		"secform4 /get_institution_holders": {output: payload},
		"defillama /equities/v1/filings": {output: map[string]any{"filings": []any{
			map[string]any{"form": "10-K", "primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl.htm"},
		}}},
	})
	result := mustCall(t, svc, "get_institutional_investors", map[string]any{"ticker": "AAPL"})
	records := asRecords(t, result.Value)
	if len(records) != 2 {
		t.Fatalf("expected 2 distinct investors, got %d: %#v", len(records), records)
	}

	filtered := mustCall(t, svc, "get_institutional_investors", map[string]any{"ticker": "AAPL", "name": "berk"})
	filteredRecords := asRecords(t, filtered.Value)
	if len(filteredRecords) != 1 {
		t.Fatalf("expected 1 investor matching 'berk', got %d", len(filteredRecords))
	}
	// The upstream feed spells these filer_name/filer_cik; the response
	// must carry the Financial Datasets InstitutionalInvestor contract's
	// own name/cik pair for this route.
	row := jsonRoundTrip(t, filteredRecords[0])
	if row["name"] != "Berkshire Hathaway Inc" || row["cik"] != "0001067983" {
		t.Fatalf("unexpected investor: %#v", row)
	}
	if _, present := row["filer_name"]; present {
		t.Fatalf("expected the upstream filer_name spelling not to leak into the response: %#v", row)
	}
}

// --- getKPISectors ---

func TestGetKPISectors_BatchResolvesAndCapsInput(t *testing.T) {
	payload := map[string]any{"status": "success", "data": map[string]any{"results": []any{
		map[string]any{"ticker": "AAPL", "sector": "Technology"},
		map[string]any{"ticker": "JPM", "sector": "Financial Services"},
	}}}
	svc, transport := newTestService(t, map[string]fakeOutcome{
		"finviz /get_ticker_sectors_with_performance": {output: payload},
	})
	cc := svc.newCallCtx(context.Background(), "key", "test")
	result, err := cc.getKPISectors(map[string]any{"tickers": []any{"AAPL", "JPM"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(records))
	}
	row := jsonRoundTrip(t, records[0])
	if row["ticker"] != "AAPL" || row["sector"] != "Technology" {
		t.Fatalf("unexpected row: %#v", row)
	}
	if transport.CallCount() != 1 {
		t.Fatalf("expected exactly one batched call, got %d", transport.CallCount())
	}

	tooMany := make([]any, 101)
	for i := range tooMany {
		tooMany[i] = "T"
	}
	if _, err := cc.getKPISectors(map[string]any{"tickers": tooMany}); err == nil {
		t.Fatalf("expected an error for more than 100 tickers")
	}
}

// --- getIPOCalendar ---

func TestGetIPOCalendar_FlattensBucketsAndAllowsEmpty(t *testing.T) {
	payload := map[string]any{"status": "success", "data": map[string]any{
		"thisWeek": []any{
			map[string]any{"companyName": "Example Corp", "symbol": "EXMP", "exchange": "NASDAQ", "expectedPrice": "$18.00"},
		},
		"nextWeek": []any{},
		"later":    []any{},
	}}
	svc, _ := newTestService(t, map[string]fakeOutcome{
		"stockanalysis /get_ipo_calendar": {output: payload},
	})
	cc := svc.newCallCtx(context.Background(), "key", "test")
	result, err := cc.getIPOCalendar(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	row := jsonRoundTrip(t, records[0])
	if row["company_name"] != "Example Corp" || row["ticker"] != "EXMP" || row["bucket"] != "this_week" {
		t.Fatalf("unexpected row: %#v", row)
	}
	if row["expected_offering_price"] != 18.0 {
		t.Fatalf("expected parsed offering price 18.0, got %#v", row["expected_offering_price"])
	}
}

func TestGetIPOCalendar_AllEmptyIsNotAnError(t *testing.T) {
	payload := map[string]any{"status": "success", "data": map[string]any{
		"thisWeek": []any{}, "nextWeek": []any{}, "later": []any{},
	}}
	svc, _ := newTestService(t, map[string]fakeOutcome{
		"stockanalysis /get_ipo_calendar": {output: payload},
	})
	cc := svc.newCallCtx(context.Background(), "key", "test")
	result, err := cc.getIPOCalendar(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error for a legitimately empty calendar: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 0 {
		t.Fatalf("expected 0 records, got %d", len(records))
	}
}

// --- getMarketSnapshot ---

func TestGetMarketSnapshot_ParsesRowsAndKeepsAsOf(t *testing.T) {
	payload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"data": map[string]any{
				"STOCKS": map[string]any{
					"MostActiveByShareVolume": map[string]any{
						"dataAsOf":           "Sep 4, 2026 2:18 PM ET",
						"lastTradeTimestamp": "2026-09-04T14:18:00-04:00",
						"table": map[string]any{
							"rows": []any{
								map[string]any{
									"symbol": "SNDL", "name": "Sundial Growers Inc.",
									"lastSalePrice": "$1.8399", "lastSaleChange": "+0.05",
									"change": "12,345,678",
								},
								map[string]any{
									// A row with an unparseable price: must omit, not guess.
									"symbol": "XYZ", "name": "Example", "lastSalePrice": "N/A", "change": "1,000",
								},
							},
						},
					},
				},
			},
		},
	}
	svc, _ := newTestService(t, map[string]fakeOutcome{
		"nasdaq /get_market_movers": {output: payload},
	})
	cc := svc.newCallCtx(context.Background(), "key", "test")
	result, err := cc.getMarketSnapshot(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	// Financial Datasets sends only the snapshots on this route, measured
	// live 2026-09-05, so the feed's as-of label is parsed but not emitted.
	if _, present := body["data_as_of"]; present {
		t.Fatalf("data_as_of must not reach the response: %#v", body)
	}
	rows, ok := body["snapshots"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("expected 2 rows under the Financial Datasets \"snapshots\" key, got %#v", body["snapshots"])
	}
	first := rows[0].(map[string]any)
	if first["ticker"] != "SNDL" || first["price"] != 1.8399 {
		t.Fatalf("unexpected first row: %#v", first)
	}
	if first["day_change"] != 0.05 {
		t.Fatalf("expected day_change parsed from lastSaleChange, got %#v", first["day_change"])
	}
	// day_change_percent is derived from the two reported figures against
	// the implied previous close (1.8399 - 0.05), not sourced directly.
	if pct, _ := first["day_change_percent"].(float64); math.Abs(pct-2.793452148164702) > 1e-9 {
		t.Fatalf("expected day_change_percent derived from the implied previous close, got %#v", first["day_change_percent"])
	}
	if first["time"] != "2026-09-04T18:18:00Z" {
		t.Fatalf("expected the feed's trade timestamp normalized to UTC, got %#v", first["time"])
	}
	if first["time_milliseconds"] != 1788545880000.0 {
		t.Fatalf("expected time_milliseconds for the same instant, got %#v", first["time_milliseconds"])
	}
	if first["share_volume"] != 12345678.0 {
		t.Fatalf("expected share_volume parsed from the misleadingly named 'change' field, got %#v", first["share_volume"])
	}
	if first["name"] != "Sundial Growers Inc." {
		t.Fatalf("expected the nasdaq company name kept alongside the contract fields, got %#v", first["name"])
	}
	second := rows[1].(map[string]any)
	if _, present := second["price"]; present {
		t.Fatalf("expected price omitted for an unparseable value, got %#v", second["price"])
	}
	// With no price and no change there is no defined percentage; it must
	// be omitted rather than reported as zero.
	if _, present := second["day_change_percent"]; present {
		t.Fatalf("expected day_change_percent omitted when the row has no price, got %#v", second["day_change_percent"])
	}
}

// TestGetMarketSnapshot_OmitsPercentOnZeroPreviousClose pins the one
// arithmetic edge in dayChangePercent: a day change equal to the price
// implies a previous close of zero, which has no defined percentage.
func TestGetMarketSnapshot_OmitsPercentOnZeroPreviousClose(t *testing.T) {
	payload := map[string]any{
		"status": "success",
		"data": map[string]any{
			"data": map[string]any{
				"STOCKS": map[string]any{
					"MostActiveByShareVolume": map[string]any{
						"lastTradeTimestamp": "2026-09-04T14:18:00-04:00",
						"table": map[string]any{
							"rows": []any{
								map[string]any{
									"symbol": "ZERO", "lastSalePrice": "$2.00", "lastSaleChange": "+2.00",
								},
							},
						},
					},
				},
			},
		},
	}
	svc, _ := newTestService(t, map[string]fakeOutcome{
		"nasdaq /get_market_movers": {output: payload},
	})
	cc := svc.newCallCtx(context.Background(), "key", "test")
	result, err := cc.getMarketSnapshot(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	rows := body["snapshots"].([]any)
	row := rows[0].(map[string]any)
	if _, present := row["day_change_percent"]; present {
		t.Fatalf("expected day_change_percent omitted for a zero previous close, got %#v", row["day_change_percent"])
	}
}
