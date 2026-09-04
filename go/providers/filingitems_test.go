// filingitems_test.go ports tests/test_filing_items.py's coverage of the
// pure, deterministic parser and validation logic in filingitems.go: the
// static SEC catalogs, request validation (including the 10-Q Part I/Part
// II ambiguity rule), accession/URL helpers, deterministic filing
// selection, the Context.dev scrape envelope parser, and the noisy-markdown
// section extractor (inline XBRL noise plus a table of contents ahead of
// the real body). It does not port the full FinanceService orchestration
// (Monid calls, receipts ledger): that lives outside filingitems.go's
// scope, per this file's port instructions.
package providers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/belazy/monid-finance/fd"
)

// ptrString and ptrInt build pointer literals for the optional
// (quarter/item/accession) parameters that mirror Python's None.
func ptrString(s string) *string { return &s }
func ptrInt(i int) *int          { return &i }

// --- Static catalog values -------------------------------------------------

func TestFilingItemsCatalogVersionsAndSources(t *testing.T) {
	cases := []struct {
		filingType     string
		catalogVersion string
		formRevision   string
		sourceURL      string
		itemCount      int
	}{
		{"10-K", "SEC-1673-02-25", "SEC 1673 (02-25)", "https://www.sec.gov/files/form10-k.pdf", 23},
		{"10-Q", "SEC-1296-02-25", "SEC 1296 (02-25)", "https://www.sec.gov/files/form10-q.pdf", 11},
		{"8-K", "SEC-873-02-25", "SEC 873 (02-25)", "https://www.sec.gov/files/form8-k.pdf", 32},
	}
	for _, tc := range cases {
		catalog, ok := Catalogs[tc.filingType]
		if !ok {
			t.Fatalf("Catalogs missing %s", tc.filingType)
		}
		if catalog.CatalogVersion != tc.catalogVersion {
			t.Errorf("%s catalog_version = %q, want %q", tc.filingType, catalog.CatalogVersion, tc.catalogVersion)
		}
		if catalog.FormRevision != tc.formRevision {
			t.Errorf("%s form_revision = %q, want %q", tc.filingType, catalog.FormRevision, tc.formRevision)
		}
		if catalog.SourceURL != tc.sourceURL {
			t.Errorf("%s source_url = %q, want %q", tc.filingType, catalog.SourceURL, tc.sourceURL)
		}
		if len(catalog.Items) != tc.itemCount {
			t.Errorf("%s has %d items, want %d", tc.filingType, len(catalog.Items), tc.itemCount)
		}
	}
}

func TestFilingItemsCatalogSpotCheckItems(t *testing.T) {
	find := func(items []FilingItem, name string) (FilingItem, bool) {
		for _, item := range items {
			if item.Name == name {
				return item, true
			}
		}
		return FilingItem{}, false
	}

	tenK, ok := find(TenKItems, "Item-1A")
	if !ok || tenK.Number != "1A" || tenK.Title != "Risk Factors" || tenK.Part != "" {
		t.Errorf("10-K Item-1A = %+v", tenK)
	}
	tenQ1, ok := find(TenQItems, "Part-I-Item-1")
	if !ok || tenQ1.Number != "1" || tenQ1.Title != "Financial Statements" || tenQ1.Part != "I" {
		t.Errorf("10-Q Part-I-Item-1 = %+v", tenQ1)
	}
	tenQ2, ok := find(TenQItems, "Part-II-Item-1")
	if !ok || tenQ2.Number != "1" || tenQ2.Title != "Legal Proceedings" || tenQ2.Part != "II" {
		t.Errorf("10-Q Part-II-Item-1 = %+v", tenQ2)
	}
	eightK, ok := find(EightKItems, "Item-2.02")
	if !ok || eightK.Number != "2.02" || eightK.Title != "Results of Operations and Financial Condition" {
		t.Errorf("8-K Item-2.02 = %+v", eightK)
	}
}

func TestCatalogScopeIsStatic(t *testing.T) {
	if CatalogScope != "Static SEC form-instruction catalog; no Monid upstream call or claim." {
		t.Errorf("CatalogScope = %q", CatalogScope)
	}
}

// --- ValidateFilingItemRequest / ValidateCatalogFilingType ------------------

func TestValidateFilingItemRequestYearBounds(t *testing.T) {
	_, _, _, _, _, err := ValidateFilingItemRequest("aapl", "10-K", 1900, nil, nil)
	assertInputError(t, err)

	currentYearPlusOne := time.Now().Year() + 1
	_, _, _, _, _, err = ValidateFilingItemRequest("aapl", "10-K", currentYearPlusOne+1, nil, nil)
	assertInputError(t, err)

	if _, _, _, _, _, err := ValidateFilingItemRequest("aapl", "10-K", MinFilingYear, nil, nil); err != nil {
		t.Errorf("MinFilingYear should be accepted: %v", err)
	}
	if _, _, _, _, _, err := ValidateFilingItemRequest("aapl", "10-K", currentYearPlusOne, nil, nil); err != nil {
		t.Errorf("current year + 1 should be accepted: %v", err)
	}
}

func TestValidateFilingItemRequestQuarterBounds(t *testing.T) {
	_, _, _, _, _, err := ValidateFilingItemRequest("aapl", "10-Q", 2025, ptrInt(5), nil)
	assertInputError(t, err)
	_, _, _, _, _, err = ValidateFilingItemRequest("aapl", "10-Q", 2025, ptrInt(0), nil)
	assertInputError(t, err)
	if _, _, _, _, _, err := ValidateFilingItemRequest("aapl", "10-Q", 2025, ptrInt(4), nil); err != nil {
		t.Errorf("quarter 4 should be accepted: %v", err)
	}
}

func TestValidateFilingItemRequestUnknownFilingType(t *testing.T) {
	_, _, _, _, _, err := ValidateFilingItemRequest("aapl", "S-1", 2025, nil, nil)
	assertInputError(t, err)
}

func TestValidateFilingItemRequestUnknownItem(t *testing.T) {
	_, _, _, _, _, err := ValidateFilingItemRequest("aapl", "10-K", 2025, nil, ptrString("Item-99"))
	assertInputError(t, err)
	if !strings.Contains(err.Error(), "Item-1") {
		t.Errorf("error should list supported names, got %q", err.Error())
	}
}

func TestValidateFilingItemRequestNormalizesAndResolves(t *testing.T) {
	symbol, filingType, year, quarter, item, err := ValidateFilingItemRequest(
		"aapl", "10-k", 2025, ptrInt(2), ptrString("item-1a"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if symbol != "AAPL" || filingType != "10-K" || year != 2025 || quarter == nil || *quarter != 2 {
		t.Fatalf("unexpected result: symbol=%q filingType=%q year=%d quarter=%v", symbol, filingType, year, quarter)
	}
	if item == nil || item.Name != "Item-1A" || item.Title != "Risk Factors" {
		t.Fatalf("unexpected item: %+v", item)
	}
}

func TestValidateCatalogFilingTypeNormalizesCase(t *testing.T) {
	normalized, err := ValidateCatalogFilingType(" 10-q ")
	if err != nil || normalized != "10-Q" {
		t.Fatalf("ValidateCatalogFilingType(' 10-q ') = (%q, %v)", normalized, err)
	}
}

// --- ResolveItem / the 10-Q Part I / Part II ambiguity rule -----------------

func TestResolveItemTenQBareNumberIsAmbiguous(t *testing.T) {
	_, err := ResolveItem("10-Q", "Item-1")
	assertInputError(t, err)
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected an ambiguity error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Part-I-Item-1") || !strings.Contains(err.Error(), "Part-II-Item-1") {
		t.Errorf("ambiguity error should name both candidates, got %q", err.Error())
	}
}

func TestResolveItemTenQExplicitPartSpellings(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Part I Item 1", "Part-I-Item-1"},
		{"Part-1,Item-1", "Part-I-Item-1"},
		{"Part-II-Item-1", "Part-II-Item-1"},
	}
	for _, tc := range cases {
		item, err := ResolveItem("10-Q", tc.input)
		if err != nil {
			t.Errorf("ResolveItem(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if item.Name != tc.want {
			t.Errorf("ResolveItem(%q).Name = %q, want %q", tc.input, item.Name, tc.want)
		}
	}
}

func TestResolveItemTenKUnambiguousBareNumber(t *testing.T) {
	// 10-K has no parts, so a bare number/alias is never ambiguous.
	item, err := ResolveItem("10-K", "Item-1A")
	if err != nil || item.Name != "Item-1A" {
		t.Fatalf("ResolveItem(10-K, Item-1A) = (%+v, %v)", item, err)
	}
}

// --- NormalizeAccession / DeriveAccession / ValidateSECURL ------------------

const secURL1 = "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20250927.htm"
const secURL2 = "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250329.htm"

func TestNormalizeAccessionAcceptsDigitsWithOrWithoutDashes(t *testing.T) {
	for _, input := range []string{"000032019325000079", "0000320193-25-000079"} {
		got, err := NormalizeAccession(ptrString(input))
		if err != nil {
			t.Fatalf("NormalizeAccession(%q) error: %v", input, err)
		}
		if got == nil || *got != "0000320193-25-000079" {
			t.Fatalf("NormalizeAccession(%q) = %v", input, got)
		}
	}
}

func TestNormalizeAccessionNilIsNil(t *testing.T) {
	got, err := NormalizeAccession(nil)
	if err != nil || got != nil {
		t.Fatalf("NormalizeAccession(nil) = (%v, %v)", got, err)
	}
}

func TestNormalizeAccessionRejectsGarbage(t *testing.T) {
	_, err := NormalizeAccession(ptrString("not-an-accession"))
	assertInputError(t, err)
}

func TestDeriveAccessionFromFilingURL(t *testing.T) {
	got := DeriveAccession(secURL1)
	if got == nil || *got != "0000320193-25-000079" {
		t.Fatalf("DeriveAccession(secURL1) = %v", got)
	}
	if got := DeriveAccession("https://www.sec.gov/Archives/edgar/data/320193/aapl.htm"); got != nil {
		t.Fatalf("DeriveAccession(no accession) = %v, want nil", got)
	}
}

func TestValidateSECURLAcceptsWellFormedArchivesURL(t *testing.T) {
	got, err := ValidateSECURL(secURL1)
	if err != nil || got != secURL1 {
		t.Fatalf("ValidateSECURL(secURL1) = (%q, %v)", got, err)
	}
}

func TestValidateSECURLRejectsUnsafeURLs(t *testing.T) {
	cases := []string{
		"http://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl.htm",
		"https://example.com/Archives/edgar/data/320193/000032019325000079/aapl.htm",
		"https://www.sec.gov/edgar/data/320193/000032019325000079/aapl.htm",
		"https://user:pass@www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl.htm",
		"https://www.sec.gov:8443/Archives/edgar/data/320193/000032019325000079/aapl.htm",
		"https://www.sec.gov/Archives/edgar/data/320193/aapl.htm", // no accession number
	}
	for _, bad := range cases {
		if _, err := ValidateSECURL(bad); err == nil {
			t.Errorf("ValidateSECURL(%q) should have failed", bad)
		}
	}
}

// --- SelectFiling: deterministic accession/period selection -----------------

func filingRow(reportDate, filingDate, form, url string) map[string]any {
	return map[string]any{
		"filingDate":          filingDate,
		"reportDate":          reportDate,
		"form":                form,
		"primaryDocumentUrl":  url,
		"documentDescription": form + " annual report",
	}
}

func TestSelectFilingPicksRequestedAccessionAndQuarter(t *testing.T) {
	filings := []any{
		filingRow("2025-03-29", "2025-05-01", "10-Q", secURL2),
		filingRow("2025-09-27", "2025-10-31", "10-Q", secURL1),
		filingRow("2024-09-28", "2024-11-01", "10-Q", secURL1),
	}
	selection, err := SelectFiling(filings, "10-Q", 2025, ptrInt(1), ptrString("0000320193-25-000010"))
	if err != nil {
		t.Fatalf("SelectFiling error: %v", err)
	}
	if selection.MatchingCount != 1 {
		t.Fatalf("MatchingCount = %d, want 1", selection.MatchingCount)
	}
	if selection.Filing == nil {
		t.Fatal("Filing is nil")
	}
	if selection.Filing.AccessionNumber != "0000320193-25-000010" {
		t.Errorf("AccessionNumber = %q", selection.Filing.AccessionNumber)
	}
	if selection.Filing.ReportDate != "2025-03-29" || selection.Filing.FilingDate != "2025-05-01" {
		t.Errorf("unexpected dates: %+v", selection.Filing)
	}
}

func TestSelectFilingNewestFirstWithoutAccessionOrQuarter(t *testing.T) {
	filings := []any{
		filingRow("2025-03-29", "2025-05-01", "10-Q", secURL2),
		filingRow("2025-09-27", "2025-10-31", "10-Q", secURL1),
		filingRow("2024-09-28", "2024-11-01", "10-Q", secURL1),
	}
	selection, err := SelectFiling(filings, "10-Q", 2025, nil, nil)
	if err != nil {
		t.Fatalf("SelectFiling error: %v", err)
	}
	if selection.MatchingCount != 2 {
		t.Fatalf("MatchingCount = %d, want 2", selection.MatchingCount)
	}
	if selection.Filing == nil || selection.Filing.AccessionNumber != "0000320193-25-000079" {
		t.Fatalf("expected the newest (Sept) filing, got %+v", selection.Filing)
	}
}

func TestSelectFilingNoMatchReturnsNilFiling(t *testing.T) {
	filings := []any{filingRow("2025-09-27", "2025-10-31", "10-Q", secURL1)}
	selection, err := SelectFiling(filings, "10-K", 2025, nil, nil)
	if err != nil {
		t.Fatalf("SelectFiling error: %v", err)
	}
	if selection.Filing != nil || selection.MatchingCount != 0 {
		t.Fatalf("expected no match, got %+v", selection)
	}
}

func TestSelectFilingUnwrapsNestedDataAndFilingsKeys(t *testing.T) {
	nested := map[string]any{
		"data": map[string]any{
			"filings": []any{filingRow("2025-09-27", "2025-10-31", "10-K", secURL1)},
		},
	}
	selection, err := SelectFiling(nested, "10-K", 2025, nil, nil)
	if err != nil {
		t.Fatalf("SelectFiling error: %v", err)
	}
	if selection.Filing == nil {
		t.Fatal("expected a match through nested data/filings wrappers")
	}
}

func TestSelectFilingRejectsNonObjectRows(t *testing.T) {
	_, err := SelectFiling([]any{"not-an-object"}, "10-K", 2025, nil, nil)
	assertSchemaDriftError(t, err)
}

func TestSelectFilingRejectsUnrecognizedPayloadShape(t *testing.T) {
	_, err := SelectFiling(map[string]any{"unexpected": true}, "10-K", 2025, nil, nil)
	assertSchemaDriftError(t, err)
}

// --- ParseScrapePayload ------------------------------------------------------

func TestParseScrapePayloadHappyPath(t *testing.T) {
	payload := map[string]any{
		"success":       true,
		"markdown":      "# ITEM 1. BUSINESS\nBody.\n",
		"contentLength": float64(26),
		"url":           secURL1,
		"metadata":      map[string]any{"title": "Apple 10-K"},
		"cache_metadata": map[string]any{
			"hit": false,
		},
	}
	markdown, meta, err := ParseScrapePayload(payload, secURL1)
	if err != nil {
		t.Fatalf("ParseScrapePayload error: %v", err)
	}
	if markdown != payload["markdown"] {
		t.Errorf("markdown mismatch")
	}
	if meta["url"] != secURL1 || meta["content_length"] != 26 {
		t.Errorf("unexpected meta: %+v", meta)
	}
}

func TestParseScrapePayloadUnwrapsDataEnvelope(t *testing.T) {
	payload := map[string]any{
		"data": map[string]any{
			"success":       true,
			"markdown":      "content",
			"contentLength": float64(7),
			"url":           secURL1,
		},
	}
	markdown, _, err := ParseScrapePayload(payload, secURL1)
	if err != nil || markdown != "content" {
		t.Fatalf("ParseScrapePayload(data-wrapped) = (%q, %v)", markdown, err)
	}
}

func TestParseScrapePayloadRejectsFailures(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"success":       true,
			"markdown":      "content",
			"contentLength": float64(7),
			"url":           secURL1,
		}
	}
	cases := map[string]map[string]any{
		"not success": func() map[string]any { p := base(); p["success"] = false; return p }(),
		"blank markdown": func() map[string]any {
			p := base()
			p["markdown"] = "   "
			return p
		}(),
		"negative content length": func() map[string]any {
			p := base()
			p["contentLength"] = float64(-1)
			return p
		}(),
		"non-integer content length": func() map[string]any {
			p := base()
			p["contentLength"] = 7.5
			return p
		}(),
		"missing url": func() map[string]any {
			p := base()
			delete(p, "url")
			return p
		}(),
		"unsafe url": func() map[string]any {
			p := base()
			p["url"] = "https://example.com/Archives/edgar/data/320193/000032019325000079/aapl.htm"
			return p
		}(),
		"mismatched url": func() map[string]any {
			p := base()
			p["url"] = secURL2
			return p
		}(),
	}
	for name, payload := range cases {
		if _, _, err := ParseScrapePayload(payload, secURL1); err == nil {
			t.Errorf("%s: expected an error", name)
		} else {
			assertSchemaDriftError(t, err)
		}
	}
}

func TestParseScrapePayloadRejectsNonObject(t *testing.T) {
	_, _, err := ParseScrapePayload([]any{1, 2, 3}, secURL1)
	assertSchemaDriftError(t, err)
}

// --- ParseFilingSections: the noisy 10-K fixture ----------------------------
//
// NOISY_10_K reproduces tests/test_filing_items.py's fixture: 32KB+ of
// repeated inline-XBRL noise, followed by a markdown table of contents that
// lists Item 1/1A/2 ahead of their real bodies, followed by the real body
// sections. The parser must skip the XBRL noise, recognize (and reject as
// candidates) the table-of-contents headings, and select the real body
// spans instead.

const inlineXBRLNoiseLine = `<ix:nonNumeric name="dei:EntityFileNumber" contextRef="c-1">ITEM 1A. RISK FACTORS</ix:nonNumeric>` + "\n"

func noisyTenKFixture() string {
	noise := "<ix:hidden>\n" + strings.Repeat(inlineXBRLNoiseLine, 400) + "</ix:hidden>\n"
	if len(noise) <= 32*1024 {
		panic("inline XBRL noise fixture must exceed 32KB, matching the Python fixture's guard")
	}
	return noise +
		"# Table of Contents\n" +
		"[Item 1. Business](#item-1)\n" +
		"[Item 1A. Risk Factors](#item-1a)\n" +
		"Item 2. Properties\n" +
		"# ITEM 1. BUSINESS\n" +
		"Apple designs and sells products. This is the body section.\n" +
		"# ITEM 1A. RISK FACTORS\n" +
		"Demand, competition, and supply constraints could harm results. " +
		"This sentence must be selected instead of the table of contents.\n" +
		"# ITEM 2. PROPERTIES\n" +
		"The company owns and leases facilities.\n"
}

func TestParseFilingSectionsNoisyTenKReturnsAllItemsInCatalogOrder(t *testing.T) {
	sections, err := ParseFilingSections(noisyTenKFixture(), "10-K", nil)
	if err != nil {
		t.Fatalf("ParseFilingSections error: %v", err)
	}
	wantItems := []string{"Item-1", "Item-1A", "Item-2"}
	wantTitles := []string{"Business", "Risk Factors", "Properties"}
	wantContent := []string{
		"Apple designs and sells products. This is the body section.",
		"Demand, competition, and supply constraints could harm results. " +
			"This sentence must be selected instead of the table of contents.",
		"The company owns and leases facilities.",
	}
	if len(sections) != len(wantItems) {
		t.Fatalf("got %d sections, want %d: %+v", len(sections), len(wantItems), sections)
	}
	for i, section := range sections {
		if section["item"] != wantItems[i] {
			t.Errorf("section %d item = %v, want %v", i, section["item"], wantItems[i])
		}
		if section["title"] != wantTitles[i] {
			t.Errorf("section %d title = %v, want %v", i, section["title"], wantTitles[i])
		}
		if section["content"] != wantContent[i] {
			t.Errorf("section %d content = %v, want %v", i, section["content"], wantContent[i])
		}
	}
}

func TestParseFilingSectionsNoisyTenKNeverReturnsTOCOnlyCandidate(t *testing.T) {
	sections, err := ParseFilingSections(noisyTenKFixture(), "10-K", nil)
	if err != nil {
		t.Fatalf("ParseFilingSections error: %v", err)
	}
	for _, section := range sections {
		content, _ := section["content"].(string)
		if strings.Contains(content, "........ 12") {
			t.Errorf("section content leaked a TOC dot-leader artifact: %q", content)
		}
		if strings.Contains(content, "[Item 1. Business]") {
			t.Errorf("section content leaked a TOC markdown link: %q", content)
		}
	}
}

func TestParseFilingSectionsHonorsRequestedItem(t *testing.T) {
	riskFactors, err := ResolveItem("10-K", "Item-1A")
	if err != nil {
		t.Fatalf("ResolveItem error: %v", err)
	}
	sections, err := ParseFilingSections(noisyTenKFixture(), "10-K", &riskFactors)
	if err != nil {
		t.Fatalf("ParseFilingSections error: %v", err)
	}
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(sections), sections)
	}
	if sections[0]["item"] != "Item-1A" || sections[0]["part"] != nil {
		t.Errorf("unexpected section: %+v", sections[0])
	}
}

func TestParseFilingSectionsMissingRequestedItemReturnsNoSections(t *testing.T) {
	item, err := ResolveItem("10-K", "Item-7")
	if err != nil {
		t.Fatalf("ResolveItem error: %v", err)
	}
	sections, err := ParseFilingSections("# ITEM 1. BUSINESS\nOnly business appears.\n", "10-K", &item)
	if err != nil {
		t.Fatalf("ParseFilingSections error: %v", err)
	}
	if len(sections) != 0 {
		t.Fatalf("expected no sections, got %+v", sections)
	}
}

func TestParseFilingSectionsEmptyMarkdownIsSchemaDrift(t *testing.T) {
	_, err := ParseFilingSections("   \n  ", "10-K", nil)
	assertSchemaDriftError(t, err)
}

func TestParseFilingSectionsTenQPartsProduceDistinctSections(t *testing.T) {
	markdown := "# PART I\n# ITEM 1. FINANCIAL STATEMENTS\nQuarterly statements.\n" +
		"# ITEM 2. MANAGEMENT'S DISCUSSION AND ANALYSIS OF FINANCIAL CONDITION AND " +
		"RESULTS OF OPERATIONS\nMD&A body.\n" +
		"# PART II\n# ITEM 1. LEGAL PROCEEDINGS\nNo material proceedings.\n"
	sections, err := ParseFilingSections(markdown, "10-Q", nil)
	if err != nil {
		t.Fatalf("ParseFilingSections error: %v", err)
	}
	byItem := map[string]map[string]any{}
	for _, section := range sections {
		byItem[section["item"].(string)] = section
	}
	partI1, ok := byItem["Part-I-Item-1"]
	if !ok || partI1["part"] != "I" || partI1["content"] != "Quarterly statements." {
		t.Errorf("Part-I-Item-1 = %+v", partI1)
	}
	partII1, ok := byItem["Part-II-Item-1"]
	if !ok || partII1["part"] != "II" || partII1["content"] != "No material proceedings." {
		t.Errorf("Part-II-Item-1 = %+v", partII1)
	}
	if _, ok := byItem["Part-II-Item-1A"]; ok {
		t.Errorf("Part-II-Item-1A should not appear: absent from the fixture")
	}
}

// --- CatalogPayload / ListFilingItemTypes -----------------------------------

func TestCatalogPayloadSingleFilingType(t *testing.T) {
	payload, err := CatalogPayload(ptrString("10-K"))
	if err != nil {
		t.Fatalf("CatalogPayload error: %v", err)
	}
	if payload["catalog_scope"] != CatalogScope {
		t.Errorf("catalog_scope mismatch")
	}
	catalogs, ok := payload["catalogs"].([]map[string]any)
	if !ok || len(catalogs) != 1 {
		t.Fatalf("catalogs = %+v", payload["catalogs"])
	}
	if catalogs[0]["filing_type"] != "10-K" {
		t.Errorf("catalogs[0] = %+v", catalogs[0])
	}
}

func TestCatalogPayloadAllFilingTypes(t *testing.T) {
	payload, err := CatalogPayload(nil)
	if err != nil {
		t.Fatalf("CatalogPayload error: %v", err)
	}
	catalogs, ok := payload["catalogs"].([]map[string]any)
	if !ok || len(catalogs) != 3 {
		t.Fatalf("catalogs = %+v", payload["catalogs"])
	}
}

func TestCatalogPayloadRejectsUnknownFilingType(t *testing.T) {
	_, err := CatalogPayload(ptrString("S-1"))
	assertInputError(t, err)
}

func TestListFilingItemTypesSingleFormHasExpectedShape(t *testing.T) {
	response, err := ListFilingItemTypes(ptrString("10-Q"))
	if err != nil {
		t.Fatalf("ListFilingItemTypes error: %v", err)
	}
	entries, ok := response["10-Q"]
	if !ok {
		t.Fatalf("response missing 10-Q: %+v", response)
	}
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name] = true
		if entry.Description != entry.Title {
			t.Errorf("entry %+v: description should mirror title", entry)
		}
	}
	if !names["Part-I-Item-1"] || !names["Part-II-Item-1"] {
		t.Errorf("expected both Part-I-Item-1 and Part-II-Item-1, got %+v", names)
	}
	raw, err := json.Marshal(entries[0])
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	assertExactKeys(t, asMap, "name", "title", "description")
}

func TestListFilingItemTypesNilFilterKeysAllForms(t *testing.T) {
	response, err := ListFilingItemTypes(nil)
	if err != nil {
		t.Fatalf("ListFilingItemTypes error: %v", err)
	}
	for _, form := range []string{"10-K", "10-Q", "8-K"} {
		if _, ok := response[form]; !ok {
			t.Errorf("response missing %s", form)
		}
	}
	if len(response) != 3 {
		t.Errorf("response has %d keys, want 3: %+v", len(response), response)
	}
}

func TestListFilingItemTypesRejectsUnknownForm(t *testing.T) {
	_, err := ListFilingItemTypes(ptrString("S-1"))
	assertInputError(t, err)
}

// --- FD response shape: FilingItemRecord / BuildFilingItemsResponse --------

func TestFilingItemRecordMatchesFDShape(t *testing.T) {
	record := FilingItemRecord("Item-1A", "Risk Factors", "Demand, competition, ...")
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	assertExactKeys(t, asMap, "number", "name", "text")
	if asMap["number"] != "Item-1A" || asMap["name"] != "Risk Factors" {
		t.Errorf("unexpected record: %+v", asMap)
	}
}

func TestBuildFilingItemsResponseMatchesFDShape(t *testing.T) {
	items := []fd.FilingItem{FilingItemRecord("Item-1A", "Risk Factors", "Demand, competition, ...")}
	response := BuildFilingItemsResponse(
		secURL1, "AAPL", "10-K", ptrString("0000320193-25-000079"), 2025, nil, items,
	)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	// Matches test_get_filing_items_matches_fd_response_shape's
	// set(response) assertion exactly: no "cik" key, "quarter" present and
	// null for a 10-K.
	assertExactKeys(t, asMap, "resource", "ticker", "filing_type", "accession_number", "year", "quarter", "items")
	if asMap["ticker"] != "AAPL" || asMap["filing_type"] != "10-K" {
		t.Errorf("unexpected response: %+v", asMap)
	}
	if asMap["accession_number"] != "0000320193-25-000079" {
		t.Errorf("accession_number = %v", asMap["accession_number"])
	}
	if asMap["quarter"] != nil {
		t.Errorf("quarter = %v, want null for a 10-K", asMap["quarter"])
	}
	itemsOut, ok := asMap["items"].([]any)
	if !ok || len(itemsOut) != 1 {
		t.Fatalf("items = %+v", asMap["items"])
	}
	itemOut, ok := itemsOut[0].(map[string]any)
	if !ok {
		t.Fatalf("items[0] = %+v", itemsOut[0])
	}
	assertExactKeys(t, itemOut, "number", "name", "text")
}

func TestBuildFilingItemsResponseTenQHasIntegerQuarter(t *testing.T) {
	response := BuildFilingItemsResponse(secURL2, "AAPL", "10-Q", nil, 2025, ptrInt(1), nil)
	raw, _ := json.Marshal(response)
	var asMap map[string]any
	_ = json.Unmarshal(raw, &asMap)
	if asMap["quarter"] != float64(1) {
		t.Errorf("quarter = %v, want 1", asMap["quarter"])
	}
	if asMap["accession_number"] != nil {
		t.Errorf("accession_number = %v, want null", asMap["accession_number"])
	}
	if items, ok := asMap["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want an empty list (never null)", asMap["items"])
	}
}

// --- shared assertion helpers ------------------------------------------------

func assertInputError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an InputError, got nil")
	}
	var inputErr *InputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("expected an *InputError, got %T: %v", err, err)
	}
}

func assertSchemaDriftError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a SchemaDriftError, got nil")
	}
	var schemaErr *SchemaDriftError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected a *SchemaDriftError, got %T: %v", err, err)
	}
}

func assertExactKeys(t *testing.T, m map[string]any, keys ...string) {
	t.Helper()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	if len(m) != len(want) {
		t.Fatalf("got keys %v, want exactly %v", mapKeys(m), keys)
	}
	for k := range m {
		if !want[k] {
			t.Fatalf("unexpected key %q in %v, want exactly %v", k, mapKeys(m), keys)
		}
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
