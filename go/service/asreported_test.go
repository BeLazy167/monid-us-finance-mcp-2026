package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The R-file fixtures below are trimmed copies of Apple's FY2025 10-K
// rendered statements (accession 0000320193-25-000079, captured
// 2026-09-04). They keep the exact markup EDGAR emits - the class="pl"
// label cell, the Show.showAR element reference, the class="num" negative
// and class="nump" positive value cells, the &#160; empty cell - because
// the parser reads all of those, and a tidied-up fixture would stop
// testing the real shape.

const asReportedFilingSummary = `<?xml version='1.0' encoding='utf-8'?>
<FilingSummary>
  <MyReports>
    <Report>
      <HtmlFileName>R1.htm</HtmlFileName>
      <ShortName>Cover Page</ShortName>
      <MenuCategory>Cover</MenuCategory>
    </Report>
    <Report>
      <HtmlFileName>R3.htm</HtmlFileName>
      <ShortName>CONSOLIDATED STATEMENTS OF OPERATIONS</ShortName>
      <MenuCategory>Statements</MenuCategory>
    </Report>
    <Report>
      <HtmlFileName>R4.htm</HtmlFileName>
      <ShortName>CONSOLIDATED STATEMENTS OF COMPREHENSIVE INCOME</ShortName>
      <MenuCategory>Statements</MenuCategory>
    </Report>
    <Report>
      <HtmlFileName>R5.htm</HtmlFileName>
      <ShortName>CONSOLIDATED BALANCE SHEETS</ShortName>
      <MenuCategory>Statements</MenuCategory>
    </Report>
    <Report>
      <HtmlFileName>R6.htm</HtmlFileName>
      <ShortName>CONSOLIDATED BALANCE SHEETS (Parenthetical)</ShortName>
      <MenuCategory>Statements</MenuCategory>
    </Report>
  </MyReports>
</FilingSummary>`

func showAR(element string) string {
	return `<a class="a" href="javascript:void(0);" onclick="Show.showAR( this, 'defref_` + element + `', window );">`
}

func reportRow(element, label, cell string) string {
	return `<tr class="re">
<td class="pl" style="border-bottom: 0px;" valign="top">` + showAR(element) + label + `</a></td>
` + cell + `
</tr>`
}

func numCell(text string) string { return `<td class="nump">` + text + `<span></span></td>` }
func negCell(text string) string { return `<td class="num">` + text + `<span></span></td>` }
func textCell() string           { return `<td class="text">&#160;<span></span></td>` }
func memberRow(el, l string) string {
	return `<tr class="rh">
<td class="pl" style="border-bottom: 0px;" valign="top">` + showAR(el) + l + `</a></td>
` + textCell() + `
</tr>`
}

// asReportedIncomeFixture keeps every structural case the parser has to get
// right in one table: an Abstract section closed by its own total, an
// unscaled per-share row and a separately scaled share-count row under the
// same header, a parenthesised negative, and a dimension-member section.
var asReportedIncomeFixture = `<html><body>
<table class="report" border="0" cellspacing="2" id="id2">
<tr>
<th class="tl" colspan="1" rowspan="2"><div style="width: 200px;"><strong>CONSOLIDATED STATEMENTS OF OPERATIONS - USD ($)<br> shares in Thousands, $ in Millions</strong></div></th>
<th class="th" colspan="2">12 Months Ended</th>
</tr>
<tr>
<th class="th"><div>Sep. 27, 2025</div></th>
<th class="th"><div>Sep. 28, 2024</div></th>
</tr>
` + reportRow("us-gaap_RevenueFromContractWithCustomerExcludingAssessedTax", "Net sales", numCell("$ 416,161")+numCell("$ 391,035")) + `
` + reportRow("us-gaap_OperatingExpensesAbstract", "<strong>Operating expenses:</strong>", textCell()+textCell()) + `
` + reportRow("us-gaap_ResearchAndDevelopmentExpense", "Research and development", numCell("34,550")+numCell("31,370")) + `
` + reportRow("us-gaap_OperatingExpenses", "Total operating expenses", numCell("62,151")+numCell("57,467")) + `
` + reportRow("us-gaap_NonoperatingIncomeExpense", "Other income/(expense), net", negCell("(321)")+numCell("269")) + `
` + reportRow("us-gaap_EarningsPerShareBasic", "Basic (in dollars per share)", numCell("$ 7.49")+numCell("$ 6.11")) + `
` + reportRow("us-gaap_WeightedAverageNumberOfSharesOutstandingBasic", "Basic (in shares)", numCell("14,948,500")+numCell("15,343,783")) + `
` + memberRow("srt_ProductOrServiceAxis=us-gaap_ProductMember", "Products") + `
` + reportRow("us-gaap_RevenueFromContractWithCustomerExcludingAssessedTax", "Net sales", numCell("$ 307,003")+numCell("$ 294,866")) + `
</table>
<table border="0" class="authRefData" style="display: none;" id="defref_us-gaap_OperatingExpenses">
<tr><td>` + reportRow("us-gaap_ShouldNeverBeParsed", "Definition popup", numCell("999")) + `</td></tr>
</table>
</body></html>`

// asReportedBalanceFixture covers the nesting case the income statement
// cannot: two sections in sequence, each closed by its own total, followed
// by a grand total that belongs to neither.
var asReportedBalanceFixture = `<html><body>
<table class="report" border="0" cellspacing="2" id="id2">
<tr>
<th class="tl" colspan="1" rowspan="2"><div style="width: 200px;"><strong>CONSOLIDATED BALANCE SHEETS - USD ($)<br> $ in Thousands</strong></div></th>
<th class="th" colspan="1">Sep. 27, 2025</th>
</tr>
` + reportRow("us-gaap_AssetsCurrentAbstract", "<strong>Current assets:</strong>", textCell()) + `
` + reportRow("us-gaap_CashAndCashEquivalentsAtCarryingValue", "Cash and cash equivalents", numCell("$ 35,934")) + `
` + reportRow("us-gaap_AssetsCurrent", "Total current assets", numCell("147,957")) + `
` + reportRow("us-gaap_AssetsNoncurrentAbstract", "<strong>Non-current assets:</strong>", textCell()) + `
` + reportRow("us-gaap_PropertyPlantAndEquipmentNet", "Property, plant and equipment, net", numCell("49,834")) + `
` + reportRow("us-gaap_AssetsNoncurrent", "Total non-current assets", numCell("211,284")) + `
` + reportRow("us-gaap_Assets", "Total assets", numCell("359,241")) + `
</table>
</body></html>`

// asReportedFilings is a filings feed carrying one 10-K and one 10-Q for
// AAPL, the join this route reads accession numbers and report dates from.
var asReportedFilings = []any{
	map[string]any{
		"filingDate": "2025-10-31", "reportDate": "2025-09-27", "form": "10-K",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20250927.htm",
	},
	map[string]any{
		"filingDate": "2026-01-30", "reportDate": "2025-12-27", "form": "10-Q",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019326000006/aapl-20251227.htm",
	},
}

// scrapeByURL is an http.RoundTripper for the Monid client that answers a
// context.dev scrape from a per-URL fixture table and every other
// provider/endpoint from a fixed outcome. The shared fakeTransport keys
// outcomes by provider+endpoint alone, which cannot distinguish this
// route's several scrapes of different EDGAR documents.
type scrapeByURL struct {
	t         *testing.T
	documents map[string]string
	filings   any
	requested []string
}

func (s *scrapeByURL) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Provider string `json:"provider"`
		Endpoint string `json:"endpoint"`
		Input    struct {
			QueryParams map[string]any `json:"queryParams"`
		} `json:"input"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	var output any
	switch payload.Endpoint {
	case filingsEndpoint:
		output = s.filings
	case scrapeHTMLEndpoint:
		url, _ := payload.Input.QueryParams["url"].(string)
		s.requested = append(s.requested, url)
		document, ok := s.documents[url]
		output = map[string]any{"success": ok, "html": document}
	default:
		s.t.Fatalf("unexpected endpoint %q", payload.Endpoint)
	}

	body, err := json.Marshal(map[string]any{
		"runId": "test", "provider": payload.Provider, "endpoint": payload.Endpoint,
		"status": "COMPLETED", "output": output,
		"providerResponse": map[string]any{"httpStatus": 200},
		"billing":          map[string]any{"reportedCost": map[string]any{"currency": "USD", "value": 0.001, "unit": "DOLLAR"}},
		"createdAt":        "2026-01-01T00:00:00Z", "completedAt": "2026-01-01T00:00:02Z",
	})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
}

const asReportedArchiveDir = "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/"

func newAsReportedCallCtx(t *testing.T, documents map[string]string) (*callCtx, *scrapeByURL) {
	t.Helper()
	transport := &scrapeByURL{t: t, documents: documents, filings: asReportedFilings}
	svc := New(Config{HTTP: &http.Client{Transport: transport}, Allowlist: allowAll{}, MaxConcurrentRuns: 8})
	return svc.newCallCtx(context.Background(), "key", "get_as_reported"), transport
}

func fullAsReportedDocuments() map[string]string {
	return map[string]string{
		asReportedArchiveDir + "FilingSummary.xml": asReportedFilingSummary,
		asReportedArchiveDir + "R3.htm":            asReportedIncomeFixture,
		asReportedArchiveDir + "R5.htm":            asReportedBalanceFixture,
	}
}

// node walks one level of a parsed tree by label, failing the test rather
// than panicking when the label is absent.
func node(t *testing.T, items []any, label string) map[string]any {
	t.Helper()
	for _, item := range items {
		row, ok := item.(map[string]any)
		if ok && row["label"] == label {
			return row
		}
	}
	t.Fatalf("no line item labelled %q in %v", label, itemLabels(items))
	return nil
}

func itemLabels(items []any) []string {
	labels := make([]string, 0, len(items))
	for _, item := range items {
		if row, ok := item.(map[string]any); ok {
			label, _ := row["label"].(string)
			labels = append(labels, label)
		}
	}
	return labels
}

func children(t *testing.T, parent map[string]any) []any {
	t.Helper()
	kids, ok := parent["children"].([]any)
	if !ok {
		t.Fatalf("line item %v has no children array", parent["label"])
	}
	return kids
}

func incomeLineItems(t *testing.T, cc *callCtx, args map[string]any) []any {
	t.Helper()
	result, err := cc.getAsReported(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	records := asRecords(t, result.Value)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	record := jsonRoundTrip(t, records[0])
	items, ok := record["line_items"].([]any)
	if !ok {
		t.Fatalf("record carries no line_items: %#v", record)
	}
	return items
}

// TestAsReported_NestsSectionUnderItsAbstractParent pins the one structural
// claim this route makes: a section header carries no value of its own and
// owns the rows beneath it, up to and including its total.
func TestAsReported_NestsSectionUnderItsAbstractParent(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	items := incomeLineItems(t, cc, map[string]any{"ticker": "AAPL", "statement": "income"})

	parent := node(t, items, "Operating expenses:")
	if parent["value"] != nil {
		t.Fatalf("a section header must have no value of its own, got %v", parent["value"])
	}
	if parent["full_label"] != "Operating Expenses Abstract" {
		t.Fatalf("full_label = %v, want the element name split into words", parent["full_label"])
	}
	kids := children(t, parent)
	if got := itemLabels(kids); len(got) != 2 || got[0] != "Research and development" || got[1] != "Total operating expenses" {
		t.Fatalf("section children = %v, want R&D then its total as the last child", got)
	}
	// The total closes the section, so what follows it is a sibling of the
	// header, not another child.
	if node(t, items, "Other income/(expense), net")["value"] == nil {
		t.Fatal("the row after a section total must land beside the section, not inside it")
	}
}

// TestAsReported_ParenthesisedValuesAreNegative guards the sign convention
// EDGAR prints: "(321)" is negative 321, and reading it as positive would
// flip an expense into income.
func TestAsReported_ParenthesisedValuesAreNegative(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	items := incomeLineItems(t, cc, map[string]any{"ticker": "AAPL", "statement": "income"})

	value := node(t, items, "Other income/(expense), net")["value"]
	if value != -321e6 {
		t.Fatalf("value = %v, want -321000000 (parenthesised, then scaled by the header's millions)", value)
	}
}

// TestAsReported_ScalesEachRowByItsOwnDeclaredUnit covers the header's two
// independent scales plus the unscaled per-share row. Applying the money
// scale to all three would report Apple's $7.49 EPS as $7,490,000.
func TestAsReported_ScalesEachRowByItsOwnDeclaredUnit(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	items := incomeLineItems(t, cc, map[string]any{"ticker": "AAPL", "statement": "income"})

	for _, tc := range []struct {
		label string
		want  float64
	}{
		{"Net sales", 416161e6},
		{"Basic (in shares)", 14948500e3},
		{"Basic (in dollars per share)", 7.49},
	} {
		if got := node(t, items, tc.label)["value"]; got != tc.want {
			t.Fatalf("%s = %v, want %v", tc.label, got, tc.want)
		}
	}
}

// TestAsReported_BalanceSheetSectionsStaySiblings covers the sequence the
// income statement cannot: two sections each closed by their own total,
// then a grand total belonging to neither.
func TestAsReported_BalanceSheetSectionsStaySiblings(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	items := incomeLineItems(t, cc, map[string]any{"ticker": "AAPL", "statement": "balance"})

	if got := itemLabels(items); len(got) != 3 {
		t.Fatalf("top level = %v, want the two sections and the grand total", got)
	}
	if got := itemLabels(children(t, node(t, items, "Non-current assets:"))); len(got) != 2 {
		t.Fatalf("second section children = %v, want its own two rows only", got)
	}
	// $ in Thousands here, not Millions: the scale is read per statement.
	if got := node(t, items, "Total assets")["value"]; got != 359241e3 {
		t.Fatalf("total assets = %v, want 359241000", got)
	}
}

// TestAsReported_MemberSectionGroupsItsBreakdown keeps a dimension
// breakdown from flattening: without it two different "Net sales" figures
// would sit side by side at the top level.
func TestAsReported_MemberSectionGroupsItsBreakdown(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	items := incomeLineItems(t, cc, map[string]any{"ticker": "AAPL", "statement": "income"})

	products := node(t, items, "Products")
	if products["value"] != nil {
		t.Fatalf("a member header carries no value, got %v", products["value"])
	}
	if got := node(t, children(t, products), "Net sales")["value"]; got != 307003e6 {
		t.Fatalf("segment net sales = %v, want 307003000000", got)
	}
	if got := node(t, items, "Net sales")["value"]; got != 416161e6 {
		t.Fatalf("top-level net sales = %v; the segment figure must not overwrite it", got)
	}
}

// TestAsReported_MissingStatementYieldsNoRecord covers the filing that
// renders no cash flow statement. An empty tree would claim the filing
// reported nothing, which is a different thing from not rendering it.
func TestAsReported_MissingStatementYieldsNoRecord(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	result, err := cc.getAsReported(map[string]any{"ticker": "AAPL", "statement": "cash"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records := asRecords(t, result.Value); len(records) != 0 {
		t.Fatalf("expected no records, got %d", len(records))
	}
	if result.WrapperKey != "cash_flow_statements" {
		t.Fatalf("wrapperKey = %q, want cash_flow_statements", result.WrapperKey)
	}
}

// TestAsReported_CombinedRouteNullsTheStatementItCannotRead pins the
// combined route's contract: it names the statement it could not read as
// null rather than dropping the field or inventing an empty tree.
func TestAsReported_CombinedRouteNullsTheStatementItCannotRead(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	result, err := cc.getAsReported(map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Financial Datasets reuses the normalized route's envelope key on the
	// as-reported route, captured live 2026-09-04. A prefixed key would
	// break a client that only changed its base URL.
	if result.WrapperKey != "financials" {
		t.Fatalf("wrapperKey = %q, want financials", result.WrapperKey)
	}
	record := jsonRoundTrip(t, asRecords(t, result.Value)[0])
	if record["income_statement"] == nil || record["balance_sheet"] == nil {
		t.Fatalf("both readable statements must be present: %#v", record)
	}
	value, present := record["cash_flow_statement"]
	if !present || value != nil {
		t.Fatalf("cash_flow_statement = %v (present=%v), want an explicit null", value, present)
	}
}

// TestAsReported_MetadataComesFromTheFilingItRead checks the record is
// tied to the exact filing the tree was parsed out of.
func TestAsReported_MetadataComesFromTheFilingItRead(t *testing.T) {
	cc, _ := newAsReportedCallCtx(t, fullAsReportedDocuments())
	result, err := cc.getAsReported(map[string]any{"ticker": "AAPL", "statement": "income"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	record := jsonRoundTrip(t, asRecords(t, result.Value)[0])
	for field, want := range map[string]any{
		"ticker":           "AAPL",
		"report_period":    "2025-09-27",
		"fiscal_period":    "FY2025",
		"period":           "annual",
		"currency":         "USD",
		"accession_number": "0000320193-25-000079",
	} {
		if record[field] != want {
			t.Fatalf("%s = %v, want %v", field, record[field], want)
		}
	}
}

// TestAsReported_QuarterlyDerivesTheQuarterFromTheFiscalYearEnd covers the
// one derived field: Apple's fiscal year ends in September, so a period
// ending in December is Q1, not Q4.
func TestAsReported_QuarterlyDerivesTheQuarterFromTheFiscalYearEnd(t *testing.T) {
	documents := fullAsReportedDocuments()
	quarterDir := "https://www.sec.gov/Archives/edgar/data/320193/000032019326000006/"
	documents[quarterDir+"FilingSummary.xml"] = asReportedFilingSummary
	documents[quarterDir+"R3.htm"] = asReportedIncomeFixture

	cc, _ := newAsReportedCallCtx(t, documents)
	result, err := cc.getAsReported(map[string]any{"ticker": "AAPL", "statement": "income", "period": "quarterly"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	record := jsonRoundTrip(t, asRecords(t, result.Value)[0])
	// The label carries the FISCAL year, matching Financial Datasets. The
	// period ending 2025-12-27 is Apple's fiscal Q1 of FY2026, so it reads
	// 2026-Q1. Labelling it by the calendar year named a period that does
	// not exist.
	if record["fiscal_period"] != "2026-Q1" {
		t.Fatalf("fiscal_period = %v, want 2026-Q1", record["fiscal_period"])
	}
	if record["report_period"] != "2025-12-27" {
		t.Fatalf("report_period = %v, want the 10-Q's own period", record["report_period"])
	}
}

// TestAsReported_ReadsOnlyTheStatementItWasAskedFor keeps the per-statement
// routes from paying for scrapes they do not use.
func TestAsReported_ReadsOnlyTheStatementItWasAskedFor(t *testing.T) {
	cc, transport := newAsReportedCallCtx(t, fullAsReportedDocuments())
	if _, err := cc.getAsReported(map[string]any{"ticker": "AAPL", "statement": "balance"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{asReportedArchiveDir + "FilingSummary.xml", asReportedArchiveDir + "R5.htm"}
	if strings.Join(transport.requested, " ") != strings.Join(want, " ") {
		t.Fatalf("scraped %v, want only %v", transport.requested, want)
	}
}

// TestAsReported_RejectsUnsupportedArgumentsBeforeSpending guards the
// zero-cost rejections: neither a ttm period nor an unknown statement is
// answerable, and neither must reach a provider.
func TestAsReported_RejectsUnsupportedArgumentsBeforeSpending(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"ttm period":        {"ticker": "AAPL", "period": "ttm"},
		"unknown statement": {"ticker": "AAPL", "statement": "equity"},
		"missing ticker":    {"statement": "income"},
		"limit over cap":    {"ticker": "AAPL", "limit": float64(asReportedMaxLimit + 1)},
	} {
		t.Run(name, func(t *testing.T) {
			cc, transport := newAsReportedCallCtx(t, fullAsReportedDocuments())
			if _, err := cc.getAsReported(args); err == nil {
				t.Fatal("expected a bad_request error")
			}
			if len(transport.requested) != 0 {
				t.Fatalf("a rejected request must cost nothing, got %v", transport.requested)
			}
		})
	}
}

// TestAsReported_SchemaDriftWhenTheReportTableIsGone makes a changed EDGAR
// layout fail loudly. Returning an empty tree instead would read as "this
// company reported nothing".
func TestAsReported_SchemaDriftWhenTheReportTableIsGone(t *testing.T) {
	documents := fullAsReportedDocuments()
	documents[asReportedArchiveDir+"R3.htm"] = "<html><body><p>no report table here</p></body></html>"

	cc, _ := newAsReportedCallCtx(t, documents)
	_, err := cc.getAsReported(map[string]any{"ticker": "AAPL", "statement": "income"})
	if err == nil {
		t.Fatal("expected schema drift when the rendered statement carries no report table")
	}
	if !strings.Contains(err.Error(), "report table") {
		t.Fatalf("error should name what was missing, got: %v", err)
	}
}
