// This file builds the four as-reported statement routes from SEC EDGAR's
// own rendered statement files, the R{n}.htm pages EDGAR generates from a
// filing's XBRL presentation linkbase.
//
// Why EDGAR at all: no allowlisted Monid provider carries as-filed XBRL,
// and Financial Datasets' as-reported routes are the filing's own
// hierarchy, not a normalized one. EDGAR is where that hierarchy exists.
// The documents are fetched through Monid's context.dev scraper the same
// way secciks.go fetches SEC's ticker file, so every read is billed to the
// caller, ledgered, and allowlist-checked (see fetchSECDocument).
//
// What one request costs: one filings run, which shares a cache entry with
// get_filings and the normalized statement routes, plus one scrape per
// EDGAR document. The combined route reads four documents per period (the
// filing index and three statements); a per-statement route reads two.
//
// What "parity" means here, and what it does not. This route reproduces
// Financial Datasets' as-reported STRUCTURE: the same recursive
// {label, full_label, value, children} tree, the same metadata fields, the
// same envelope keys (docs/fd-openapi.json's AsReportedStatement /
// AsReportedNode). It does NOT claim label parity. Financial Datasets
// normalizes some labels away from the filing's own wording - Apple's R
// file prints "Gross margin" where Financial Datasets says "Gross Profit" -
// and this port prints what the filing prints, because that is what
// "as-reported" means. full_label is derived from the row's XBRL element
// name, so the two agree there even when the printed labels differ.
//
// What this file cannot source, and why it omits rather than guesses:
//
//   - Sibling-vs-child nesting between two consecutive section headers.
//     The R file carries no indentation (measured 2026-09-04 across
//     Apple's and Microsoft's FY10-Ks: every label cell is a bare
//     <td class="pl">), so the only structural signal is the XBRL
//     convention that a section closes on its own total row. Where a
//     filer's section header has no total row, the next header nests
//     under it rather than beside it. Apple's income statement is the one
//     visible case: "Shares used in computing earnings per share:" lands
//     inside "Earnings per share:". Everything with a total row - every
//     section of the balance sheet and the cash flow statement - nests
//     exactly right.
//   - fiscal_period for a quarterly period whose issuer has filed no
//     annual report in the same feed. The quarter number is derived from
//     the fiscal year end, and with no annual filing there is nothing to
//     anchor it to, so the field is omitted.
//   - Any statement the filing does not render as its own report. A 10-K
//     with no "CASH FLOW" report yields a null cash_flow_statement on the
//     combined route and no record at all on the per-statement one.
package service

import (
	"encoding/json"
	"encoding/xml"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

// secArchivesBase is the EDGAR archive root every filing directory hangs
// off. It is a var, not a const, for the same reason
// secCompanyTickersURL is: a test points it somewhere else.
var secArchivesBase = "https://www.sec.gov/Archives/edgar/data"

const (
	// asReportedMaxLimit bounds how many periods one request may ask for.
	// Each period costs one scrape of the filing's FilingSummary.xml plus
	// one per statement read, and every scrape is billed, so this bounds
	// what a single request can spend. It matches Financial Datasets' own
	// page size.
	asReportedMaxLimit = 10
)

// asReportedVariant selects which statement(s) one request builds. The four
// REST routes differ only in this value and in the envelope key that falls
// out of it, so they share one capability rather than four near-identical
// ones.
type asReportedVariant int

const (
	asReportedAll asReportedVariant = iota
	asReportedIncome
	asReportedBalance
	asReportedCash
)

// asReportedVariants maps the capability's "statement" argument to its
// variant and to the Financial Datasets envelope key that variant answers
// with (docs/fd-openapi.json).
var asReportedVariants = map[string]struct {
	variant    asReportedVariant
	wrapperKey string
}{
	"all":     {asReportedAll, "as_reported_financials"},
	"income":  {asReportedIncome, "as_reported_income_statements"},
	"balance": {asReportedBalance, "as_reported_balance_sheets"},
	"cash":    {asReportedCash, "as_reported_cash_flow_statements"},
}

// asReportedNode mirrors Financial Datasets' AsReportedNode schema. Every
// field is emitted even when null: the schema declares label, full_label
// and value nullable rather than optional, and a caller walking the tree
// needs children present as [] on a leaf, not absent.
//
// Children holds pointers so the builder can keep a stack of open sections
// that stays valid as siblings are appended around them.
type asReportedNode struct {
	Label     *string           `json:"label"`
	FullLabel *string           `json:"full_label"`
	Value     *float64          `json:"value"`
	Children  []*asReportedNode `json:"children"`
}

// asReportedMetadata mirrors the SegmentMetadata fields Financial Datasets'
// AsReportedStatement composes in.
type asReportedMetadata struct {
	Ticker          *string `json:"ticker,omitempty"`
	ReportPeriod    *string `json:"report_period,omitempty"`
	FiscalPeriod    *string `json:"fiscal_period,omitempty"`
	Period          *string `json:"period,omitempty"`
	Currency        *string `json:"currency,omitempty"`
	AccessionNumber *string `json:"accession_number,omitempty"`
	FilingURL       *string `json:"filing_url,omitempty"`
}

// asReportedStatementRecord is one period's tree for one statement, the
// AsReportedStatement shape the three per-statement routes return.
type asReportedStatementRecord struct {
	asReportedMetadata
	LineItems []*asReportedNode `json:"line_items"`
}

// asReportedTree is the nullable per-statement object nested inside the
// combined route's record.
type asReportedTree struct {
	LineItems []*asReportedNode `json:"line_items"`
}

// asReportedCombinedRecord is one period carrying all three trees, the
// shape /financials/as-reported returns. The three statement fields are
// emitted as null when the filing renders no such statement, which is what
// Financial Datasets' own AsReportedFinancialsResponse declares ("any one
// may be null if data is missing").
type asReportedCombinedRecord struct {
	asReportedMetadata
	IncomeStatement   *asReportedTree `json:"income_statement"`
	BalanceSheet      *asReportedTree `json:"balance_sheet"`
	CashFlowStatement *asReportedTree `json:"cash_flow_statement"`
}

// asReportedArgs is one parsed, pre-validated request.
type asReportedArgs struct {
	variant    asReportedVariant
	wrapperKey string
	ticker     string
	cik        string
	period     string
	limit      int
	report     dateFilters
}

var cikPattern = regexp.MustCompile(`^[0-9]{1,10}$`)

// parseAsReportedArgs validates the capability's arguments. period is
// annual or quarterly only: Financial Datasets' as-reported routes publish
// no ttm option, and there is no such thing as a ttm filing to report from.
func parseAsReportedArgs(args map[string]any) (asReportedArgs, error) {
	statement, err := argStringDefault(args, "statement", "all")
	if err != nil {
		return asReportedArgs{}, err
	}
	spec, ok := asReportedVariants[strings.ToLower(strings.TrimSpace(statement))]
	if !ok {
		return asReportedArgs{}, &providers.InputError{Msg: "statement must be all, income, balance, or cash"}
	}
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return asReportedArgs{}, err
	}
	if tickerArg == nil {
		return asReportedArgs{}, &providers.InputError{Msg: "ticker is required: the filings feed this route joins " +
			"against is keyed on one issuer, so a cik alone cannot locate the filing to read"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return asReportedArgs{}, err
	}
	cikArg, err := argString(args, "cik")
	if err != nil {
		return asReportedArgs{}, err
	}
	cik := ""
	if cikArg != nil {
		cik = strings.TrimSpace(*cikArg)
		if !cikPattern.MatchString(cik) {
			return asReportedArgs{}, &providers.InputError{Msg: "cik must be 1-10 digits"}
		}
	}
	periodRaw, err := argStringDefault(args, "period", "annual")
	if err != nil {
		return asReportedArgs{}, err
	}
	period := strings.ToLower(strings.TrimSpace(periodRaw))
	if period != "annual" && period != "quarterly" {
		return asReportedArgs{}, &providers.InputError{Msg: "period must be annual or quarterly; " +
			"as-reported statements come from a single filing, and no filing reports a ttm period"}
	}
	limitRaw, err := argIntDefault(args, "limit", 1)
	if err != nil {
		return asReportedArgs{}, err
	}
	limit, err := validateLimit(limitRaw, asReportedMaxLimit)
	if err != nil {
		return asReportedArgs{}, err
	}
	report, err := parseDateFilterGroup(args, "report_period")
	if err != nil {
		return asReportedArgs{}, err
	}
	return asReportedArgs{
		variant:    spec.variant,
		wrapperKey: spec.wrapperKey,
		ticker:     symbol,
		cik:        cik,
		period:     period,
		limit:      limit,
		report:     report,
	}, nil
}

// getAsReported answers get_as_reported for all four variants.
//
// The one paid call is the filings run, issued with exactly the
// provider/endpoint/params resolveIssuerCIK and the normalized statement
// routes already use, so all of them share one cache entry and one bill.
// Everything after it is free: SEC serves the rendered statements.
func (c *callCtx) getAsReported(args map[string]any) (Result, error) {
	parsed, err := parseAsReportedArgs(args)
	if err != nil {
		return Result{}, err
	}

	cik := parsed.cik
	if cik == "" {
		resolved, found, rerr := c.resolveIssuerCIK(parsed.ticker)
		if rerr != nil {
			return Result{}, rerr
		}
		if !found {
			return Result{Value: fd.NewErrorResponse("not_found",
				"No SEC CIK resolves for ticker "+parsed.ticker+", so no filing archive can be located.")}, nil
		}
		cik = resolved
	}

	run, err := c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": parsed.ticker, "country": "US"})
	if err != nil {
		return Result{}, err
	}
	filings, err := providers.NormalizeFilings(run.Output, parsed.ticker, nil, 10_000, nil, nil)
	if err != nil {
		return Result{}, err
	}
	fiscalEndMonth := annualFiscalEndMonth(filings)
	selected := selectAsReportedFilings(filings, parsed)
	if len(selected) == 0 {
		return Result{Value: fd.NewErrorResponse("not_found",
			"No "+parsed.period+" filing matches ticker "+parsed.ticker+".")}, nil
	}

	records := make([]any, 0, len(selected))
	for _, f := range selected {
		record, berr := c.buildAsReportedRecord(parsed, cik, f, fiscalEndMonth)
		if berr != nil {
			return Result{}, berr
		}
		if record != nil {
			records = append(records, record)
		}
	}
	return Result{Value: records, WrapperKey: parsed.wrapperKey, Paginate: true}, nil
}

// selectAsReportedFilings narrows the feed to the periodic filings this
// request asks for, newest first, bounded by limit.
func selectAsReportedFilings(filings []fd.Filing, parsed asReportedArgs) []fd.Filing {
	forms := annualForms
	if parsed.period == "quarterly" {
		forms = quarterlyForms
	}
	out := make([]fd.Filing, 0, parsed.limit)
	for _, f := range filings {
		if f.FilingType == nil || !forms[strings.ToUpper(*f.FilingType)] {
			continue
		}
		if f.AccessionNumber == nil || f.ReportDate == nil {
			continue
		}
		day, err := time.Parse(dateLayout, *f.ReportDate)
		if err != nil {
			continue
		}
		if parsed.report.any() && !parsed.report.matches(day) {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool { return *out[i].ReportDate > *out[j].ReportDate })
	if len(out) > parsed.limit {
		out = out[:parsed.limit]
	}
	return out
}

// buildAsReportedRecord fetches and parses one filing's rendered statements.
// It returns nil (not an error) when the per-statement variant's statement
// is absent from that filing: a record with an empty tree would assert the
// filing reported nothing, which is a different claim from "this filing
// does not render that statement".
func (c *callCtx) buildAsReportedRecord(
	parsed asReportedArgs, cik string, filing fd.Filing, fiscalEndMonth int,
) (any, error) {
	dir := secArchivesBase + "/" + strings.TrimLeft(cik, "0") + "/" +
		strings.ReplaceAll(*filing.AccessionNumber, "-", "") + "/"
	summary, err := c.fetchFilingSummary(dir + "FilingSummary.xml")
	if err != nil {
		return nil, err
	}

	meta := asReportedMetadata{
		Ticker:          filing.Ticker,
		ReportPeriod:    filing.ReportDate,
		Period:          &parsed.period,
		AccessionNumber: filing.AccessionNumber,
		FilingURL:       filing.URL,
	}
	if label := fiscalPeriodLabel(*filing.ReportDate, parsed.period, fiscalEndMonth); label != "" {
		meta.FiscalPeriod = &label
	}

	kinds := []asReportedVariant{asReportedIncome, asReportedBalance, asReportedCash}
	if parsed.variant != asReportedAll {
		kinds = []asReportedVariant{parsed.variant}
	}
	trees := map[asReportedVariant]*asReportedTree{}
	for _, kind := range kinds {
		file := summary.pick(kind)
		if file == "" {
			continue
		}
		body, ferr := c.fetchSECDocument(dir + file)
		if ferr != nil {
			return nil, ferr
		}
		table, perr := parseRenderedStatement(body)
		if perr != nil {
			return nil, perr
		}
		if meta.Currency == nil && table.currency != "" {
			currency := table.currency
			meta.Currency = &currency
		}
		trees[kind] = &asReportedTree{LineItems: table.lineItems}
	}

	if parsed.variant != asReportedAll {
		tree := trees[parsed.variant]
		if tree == nil {
			return nil, nil
		}
		return asReportedStatementRecord{asReportedMetadata: meta, LineItems: tree.LineItems}, nil
	}
	return asReportedCombinedRecord{
		asReportedMetadata: meta,
		IncomeStatement:    trees[asReportedIncome],
		BalanceSheet:       trees[asReportedBalance],
		CashFlowStatement:  trees[asReportedCash],
	}, nil
}

// --- fiscal period derivation ---

// annualFiscalEndMonth reads the issuer's fiscal year end month off the
// newest annual filing in the feed. It returns 0 when the feed carries
// none, which makes fiscalPeriodLabel omit fiscal_period for quarterly
// periods rather than guess a quarter number.
func annualFiscalEndMonth(filings []fd.Filing) int {
	newest, month := "", 0
	for _, f := range filings {
		if f.FilingType == nil || !annualForms[strings.ToUpper(*f.FilingType)] || f.ReportDate == nil {
			continue
		}
		day, err := time.Parse(dateLayout, *f.ReportDate)
		if err != nil || *f.ReportDate <= newest {
			continue
		}
		newest, month = *f.ReportDate, int(day.Month())
	}
	return month
}

// fiscalPeriodLabel names the fiscal period a report date falls in: "FY"
// for an annual filing, "Q1".."Q4" for a quarterly one, counting whole
// months from the fiscal year end. An empty string means the label could
// not be derived and the field is omitted.
func fiscalPeriodLabel(reportDate, period string, fiscalEndMonth int) string {
	if period == "annual" {
		return "FY"
	}
	if fiscalEndMonth == 0 {
		return ""
	}
	day, err := time.Parse(dateLayout, reportDate)
	if err != nil {
		return ""
	}
	elapsed := ((int(day.Month())-fiscalEndMonth)%12 + 12) % 12
	if elapsed == 0 {
		return "FY"
	}
	if elapsed%3 != 0 {
		return ""
	}
	return "Q" + strconv.Itoa(elapsed/3)
}

// --- EDGAR fetch ---

// fetchSECDocument reads one EDGAR archive document through Monid's
// context.dev scraper, the same route secciks.go's fetchSECCatalog takes
// and for the same reasons: the request is billed to the caller's own
// wallet, writes a receipts-ledger entry, and is checked against the
// discovery allowlist, none of which a direct HTTP call would do. It also
// spares the operator declaring a contact User-Agent, which sec.gov
// otherwise answers 403 without.
//
// The scraper returns raw content rather than rendered markdown, so both
// documents this route asks for - a filing's FilingSummary.xml and one
// R{n}.htm - arrive as the bytes SEC serves.
func (c *callCtx) fetchSECDocument(url string) (string, error) {
	run, err := c.run(contextDev, scrapeHTMLEndpoint, nil, map[string]any{"url": url})
	if err != nil {
		return "", err
	}
	var envelope struct {
		Success bool   `json:"success"`
		HTML    string `json:"html"`
	}
	if err := json.Unmarshal(run.Output, &envelope); err != nil {
		return "", &providers.SchemaDriftError{Msg: "context.dev scrape payload must be an object"}
	}
	if !envelope.Success || envelope.HTML == "" {
		return "", &providers.SchemaDriftError{Msg: "context.dev returned no content for " + url}
	}
	return envelope.HTML, nil
}

// filingSummary is the subset of a filing's FilingSummary.xml this route
// reads: the rendered reports and enough of each to identify the three
// primary statements.
type filingSummary struct {
	Reports []filingSummaryReport `xml:"MyReports>Report"`
}

type filingSummaryReport struct {
	HTMLFileName string `xml:"HtmlFileName"`
	ShortName    string `xml:"ShortName"`
	MenuCategory string `xml:"MenuCategory"`
}

func (c *callCtx) fetchFilingSummary(url string) (filingSummary, error) {
	body, err := c.fetchSECDocument(url)
	if err != nil {
		return filingSummary{}, err
	}
	var summary filingSummary
	if err := xml.Unmarshal([]byte(body), &summary); err != nil {
		return filingSummary{}, &providers.SchemaDriftError{
			Msg: "SEC FilingSummary.xml is not the expected report index"}
	}
	if len(summary.Reports) == 0 {
		return filingSummary{}, &providers.SchemaDriftError{
			Msg: "SEC FilingSummary.xml listed no rendered reports"}
	}
	return summary, nil
}

// pick names the rendered file holding one primary statement, or "" when
// the filing renders none.
//
// The match is on the filer's own ShortName, which is free text, so the
// tests are ordered from most to least specific: "COMPREHENSIVE INCOME"
// must not answer as the income statement, and any "(Parenthetical)"
// companion sheet (share counts and par values, not the statement) is
// excluded outright.
func (s filingSummary) pick(kind asReportedVariant) string {
	for _, report := range s.Reports {
		if !strings.EqualFold(report.MenuCategory, "Statements") || report.HTMLFileName == "" {
			continue
		}
		name := strings.ToUpper(report.ShortName)
		if strings.Contains(name, "PARENTHETICAL") {
			continue
		}
		var match bool
		switch kind {
		case asReportedCash:
			match = strings.Contains(name, "CASH FLOW")
		case asReportedBalance:
			match = strings.Contains(name, "BALANCE SHEET") || strings.Contains(name, "FINANCIAL POSITION")
		case asReportedIncome:
			match = !strings.Contains(name, "COMPREHENSIVE") &&
				(strings.Contains(name, "OPERATIONS") || strings.Contains(name, "INCOME"))
		}
		if match {
			return report.HTMLFileName
		}
	}
	return ""
}

// --- rendered statement parsing ---

// renderedStatement is one parsed R file: the line-item tree plus the
// reporting currency its header declares.
type renderedStatement struct {
	currency  string
	lineItems []*asReportedNode
}

var (
	reportTableRE  = regexp.MustCompile(`(?is)<table[^>]*\bclass="report"[^>]*>`)
	tableRowRE     = regexp.MustCompile(`(?is)<tr\b[^>]*>.*?</tr>`)
	rowClassRE     = regexp.MustCompile(`(?is)^<tr\b[^>]*\bclass="([^"]*)"`)
	headerCellRE   = regexp.MustCompile(`(?is)<th[^>]*\bclass="tl"[^>]*>(.*?)</th>`)
	elementNameRE  = regexp.MustCompile(`(?is)Show\.showAR\(\s*this,\s*'defref_([^']*)'`)
	labelAnchorRE  = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="a"[^>]*>(.*?)</a>`)
	valueCellRE    = regexp.MustCompile(`(?is)<td[^>]*\bclass="(num|nump|text)"[^>]*>(.*?)</td>`)
	tagRE          = regexp.MustCompile(`(?is)<[^>]*>`)
	moneyScaleRE   = regexp.MustCompile(`(?i)\$\s+in\s+(thousands|millions|billions)`)
	shareScaleRE   = regexp.MustCompile(`(?i)shares\s+in\s+(thousands|millions|billions)`)
	currencyCodeRE = regexp.MustCompile(`([A-Z]{3})\s*\(`)
	numericRE      = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)
)

var unitScales = map[string]float64{"thousands": 1e3, "millions": 1e6, "billions": 1e9}

// parseRenderedStatement turns one R{n}.htm file into a line-item tree.
//
// Only the first <table class="report"> is read. Every R file trails it
// with one hidden "authRefData" table per element carrying the XBRL
// definition popup; those are documentation, not data.
func parseRenderedStatement(document string) (renderedStatement, error) {
	loc := reportTableRE.FindStringIndex(document)
	if loc == nil {
		return renderedStatement{}, &providers.SchemaDriftError{
			Msg: "SEC rendered statement carries no report table"}
	}
	body := document[loc[1]:]
	if end := strings.Index(strings.ToLower(body), "</table>"); end >= 0 {
		body = body[:end]
	}

	currency, moneyScale, shareScale := parseStatementHeader(body)
	builder := newAsReportedTreeBuilder()
	for _, markup := range tableRowRE.FindAllString(body, -1) {
		row, ok := parseStatementRow(markup, moneyScale, shareScale)
		if !ok {
			continue
		}
		builder.add(row)
	}
	items := builder.finish()
	if len(items) == 0 {
		return renderedStatement{}, &providers.SchemaDriftError{
			Msg: "SEC rendered statement carried no line items"}
	}
	return renderedStatement{currency: currency, lineItems: items}, nil
}

// parseStatementHeader reads the report's title cell, which declares the
// currency and the units every figure below it is printed in, e.g.
// "CONSOLIDATED STATEMENTS OF OPERATIONS - USD ($) shares in Thousands,
// $ in Millions". Money and share counts scale independently: Apple's
// FY2025 income statement prints dollars in millions and share counts in
// thousands in the same table.
func parseStatementHeader(body string) (currency string, moneyScale, shareScale float64) {
	moneyScale, shareScale = 1, 1
	m := headerCellRE.FindStringSubmatch(body)
	if m == nil {
		return "", moneyScale, shareScale
	}
	text := cellText(m[1])
	if code := currencyCodeRE.FindStringSubmatch(text); code != nil {
		currency = code[1]
	}
	if unit := moneyScaleRE.FindStringSubmatch(text); unit != nil {
		moneyScale = unitScales[strings.ToLower(unit[1])]
	}
	if unit := shareScaleRE.FindStringSubmatch(text); unit != nil {
		shareScale = unitScales[strings.ToLower(unit[1])]
	}
	return currency, moneyScale, shareScale
}

// statementRow is one parsed data row of a rendered statement.
type statementRow struct {
	element    string
	label      string
	value      *float64
	hasValue   bool
	isAbstract bool
	isMember   bool
}

// parseStatementRow reads one <tr>. The first numeric-or-text cell is the
// most recent period: EDGAR renders columns newest first, and the
// as-reported record is keyed on the one filing it came from, so the older
// comparative columns belong to that filing's own earlier periods and are
// served by asking for those periods instead.
func parseStatementRow(markup string, moneyScale, shareScale float64) (statementRow, bool) {
	anchor := labelAnchorRE.FindStringSubmatch(markup)
	if anchor == nil {
		return statementRow{}, false
	}
	row := statementRow{label: cellText(anchor[1])}
	if m := elementNameRE.FindStringSubmatch(markup); m != nil {
		row.element = m[1]
	}
	row.isAbstract = strings.HasSuffix(row.element, "Abstract")
	if m := rowClassRE.FindStringSubmatch(markup); m != nil {
		row.isMember = strings.EqualFold(strings.TrimSpace(m[1]), "rh")
	}

	if cell := valueCellRE.FindStringSubmatch(markup); cell != nil && !strings.EqualFold(cell[1], "text") {
		row.value = parseReportedNumber(cellText(cell[2]), rowScale(row.label, moneyScale, shareScale))
		row.hasValue = row.value != nil
	}
	return row, true
}

// rowScale picks which of the header's two scales applies to a row.
//
// EDGAR annotates the label itself for exactly this reason: a share count
// reads "Basic (in shares)" and a per-share amount reads "Basic (in
// dollars per share)". Per-share amounts are printed unscaled, so applying
// the money scale to them would report Apple's $7.49 EPS as $7,490,000.
func rowScale(label string, moneyScale, shareScale float64) float64 {
	lower := strings.ToLower(label)
	switch {
	case strings.Contains(lower, "per share"):
		return 1
	case strings.Contains(lower, "(in shares)"):
		return shareScale
	default:
		return moneyScale
	}
}

// parseReportedNumber reads one rendered figure. EDGAR prints negatives in
// parentheses, prefixes the first and last figure of a block with the
// currency symbol, and groups thousands with commas.
func parseReportedNumber(text string, scale float64) *float64 {
	cleaned := strings.TrimSpace(text)
	negative := strings.HasPrefix(cleaned, "(") && strings.HasSuffix(cleaned, ")")
	if negative {
		cleaned = cleaned[1 : len(cleaned)-1]
	}
	cleaned = strings.NewReplacer("$", "", ",", "", " ", "", " ", "").Replace(cleaned)
	if cleaned == "" || !numericRE.MatchString(cleaned) {
		return nil
	}
	parsed, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return nil
	}
	value := parsed * scale
	if negative {
		value = -value
	}
	return &value
}

// cellText strips markup from one table cell and resolves HTML entities,
// so "Total shareholders&#8217; equity" reads back as the filer wrote it.
func cellText(markup string) string {
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(markup, "")))
}

// --- tree building ---

// asReportedTreeBuilder assembles the nested line-item tree from the flat
// row sequence, holding the chain of open sections as a stack.
//
// The nesting rules the R file actually supports, in the order they are
// tested:
//
//  1. A dimension-member header (the "rh" row EDGAR emits for a segment
//     breakdown, e.g. Apple's Products/Services) starts a new top-level
//     section. Its rows repeat labels already used above with different
//     values, so flattening them would put two different "Net sales"
//     figures side by side at the same level.
//  2. A row whose element name names an open section is that section's
//     total: it becomes the section's last child and closes it, along with
//     anything still open inside it. An exact name match wins over a
//     prefix match, and the prefix test exists because filers vary the
//     suffix - Apple opens "Operating activities:" as
//     NetCashProvidedByUsedInOperatingActivitiesContinuingOperationsAbstract
//     and closes it with NetCashProvidedByUsedInOperatingActivities.
//  3. A value-less row whose element name ends in "Abstract" opens a
//     section.
//  4. Anything else is a leaf under whatever is currently open.
type asReportedTreeBuilder struct {
	root  []*asReportedNode
	stack []*asReportedNode
	bases []string
}

func newAsReportedTreeBuilder() *asReportedTreeBuilder {
	return &asReportedTreeBuilder{root: []*asReportedNode{}}
}

func (b *asReportedTreeBuilder) add(row statementRow) {
	node := &asReportedNode{Value: row.value, Children: []*asReportedNode{}}
	if row.label != "" {
		label := row.label
		node.Label = &label
	}
	if full := elementFullLabel(row.element); full != "" {
		node.FullLabel = &full
	}

	// A dimension-member header carries no element name worth matching
	// totals against, so it opens a section with an empty base name, which
	// no later row can close.
	if row.isMember && !row.hasValue {
		b.closeAll()
		b.open(node, "")
		return
	}
	if depth := b.sectionClosedBy(row.element); depth >= 0 {
		b.appendTo(depth, node)
		b.closeTo(depth)
		return
	}
	if row.isAbstract && !row.hasValue {
		b.open(node, strings.TrimSuffix(row.element, "Abstract"))
		return
	}
	b.appendTo(len(b.bases)-1, node)
}

// sectionClosedBy reports the depth of the open section element totals, or
// -1 when it totals none. Open sections are searched innermost first.
func (b *asReportedTreeBuilder) sectionClosedBy(element string) int {
	if element == "" {
		return -1
	}
	for depth := len(b.bases) - 1; depth >= 0; depth-- {
		if b.bases[depth] == element {
			return depth
		}
	}
	for depth := len(b.bases) - 1; depth >= 0; depth-- {
		if b.bases[depth] != "" && strings.HasPrefix(b.bases[depth], element) {
			return depth
		}
	}
	return -1
}

// open appends a new section under whatever is currently open, then pushes
// it as the section later rows nest into.
func (b *asReportedTreeBuilder) open(node *asReportedNode, base string) {
	b.appendTo(len(b.bases)-1, node)
	b.stack = append(b.stack, node)
	b.bases = append(b.bases, base)
}

func (b *asReportedTreeBuilder) closeTo(depth int) {
	b.stack = b.stack[:depth]
	b.bases = b.bases[:depth]
}

func (b *asReportedTreeBuilder) closeAll() {
	b.stack = b.stack[:0]
	b.bases = b.bases[:0]
}

// appendTo adds node under the section at depth, or at the root when depth
// is below zero.
func (b *asReportedTreeBuilder) appendTo(depth int, node *asReportedNode) {
	if depth < 0 {
		b.root = append(b.root, node)
		return
	}
	b.stack[depth].Children = append(b.stack[depth].Children, node)
}

func (b *asReportedTreeBuilder) finish() []*asReportedNode {
	b.closeAll()
	return b.root
}

// elementFullLabel renders an XBRL element name as the canonical taxonomy
// label Financial Datasets' full_label field carries:
// "us-gaap_RevenueFromContractWithCustomerExcludingAssessedTax" becomes
// "Revenue From Contract With Customer Excluding Assessed Tax". An empty
// result leaves full_label null rather than repeating the printed label.
func elementFullLabel(element string) string {
	name := element
	if idx := strings.LastIndex(name, "_"); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" {
		return ""
	}
	var out strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		upper := r >= 'A' && r <= 'Z'
		if i > 0 && upper {
			prev := runes[i-1]
			prevUpper := prev >= 'A' && prev <= 'Z'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if !prevUpper || nextLower {
				out.WriteRune(' ')
			}
		}
		out.WriteRune(r)
	}
	return out.String()
}
