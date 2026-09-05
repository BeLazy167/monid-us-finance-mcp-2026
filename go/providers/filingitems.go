// This file (filingitems.go) ports the deterministic, non-LLM SEC
// filing-items section parser from Python:
// src/monid_finance_mcp/providers/us/filing_items.py. This is the product's
// sharpest differentiator, so fidelity to that Python implementation
// matters more than speed or Go idiom.
//
// It holds the pure catalog, validation, selection, and parsing logic: the
// static SEC item catalogs for 10-K/10-Q/8-K, request validation, SEC
// accession/URL helpers, deterministic filing selection, the Context.dev
// scrape envelope parser, and the markdown section extractor. A future
// service layer composes these with monid.Client and fd record types to
// reproduce the Monid orchestration in service.py's get_filing_items.
//
// InputError/SchemaDriftError (errors.go) and DeriveAccession (sec.go) are
// package-wide helpers shared with sibling provider files that port other
// Python modules (normalize.py, earnings.py, statements.py, ...); this file
// uses those instead of redeclaring them.
package providers

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/belazy/monid-finance/fd"
)

// MinFilingYear is the earliest year ValidateFilingItemRequest accepts,
// tracking the practical start of EDGAR full-text filings (filing_items.py's
// _MIN_FILING_YEAR).
const MinFilingYear = 1994

// CatalogScope documents that the SEC item catalogs are static form
// instructions: listing them never triggers a Monid call or claims a cost.
const CatalogScope = "Static SEC form-instruction catalog; no Monid upstream call or claim."

// FilingItem is one canonical SEC filing item: its FD item id (Name, e.g.
// "Item-1A" or "Part-I-Item-1"), its official item Number (e.g. "1A"), its
// title, and for 10-Q forms the Part ("I" or "II") that disambiguates
// repeated item numbers. Part is "" for forms with no parts (10-K, 8-K).
type FilingItem struct {
	Name   string
	Number string
	Title  string
	Part   string
}

// ToDict renders the item the way filing_items.py's FilingItem.to_dict does:
// {"name", "number", "title", "part"}, with part null when the form has no
// parts.
func (item FilingItem) ToDict() map[string]any {
	var part any
	if item.Part != "" {
		part = item.Part
	}
	return map[string]any{
		"name":   item.Name,
		"number": item.Number,
		"title":  item.Title,
		"part":   part,
	}
}

// FilingCatalog is one form's static SEC item catalog: its catalog/revision
// identifiers, the SEC source URL, and its ordered items.
type FilingCatalog struct {
	FilingType     string
	CatalogVersion string
	FormRevision   string
	SourceURL      string
	Items          []FilingItem
}

// ToDict renders the catalog the way filing_items.py's FilingCatalog.to_dict
// does.
func (c FilingCatalog) ToDict() map[string]any {
	items := make([]map[string]any, len(c.Items))
	for i, item := range c.Items {
		items[i] = item.ToDict()
	}
	return map[string]any{
		"filing_type":     c.FilingType,
		"catalog_version": c.CatalogVersion,
		"form_revision":   c.FormRevision,
		"source": map[string]any{
			"publisher": "U.S. Securities and Exchange Commission",
			"url":       c.SourceURL,
			"document":  fmt.Sprintf("Form %s instructions", c.FilingType),
		},
		"items": items,
	}
}

// TenKItems is the static SEC Form 10-K item catalog (SEC 1673 (02-25)).
var TenKItems = []FilingItem{
	{Name: "Item-1", Number: "1", Title: "Business"},
	{Name: "Item-1A", Number: "1A", Title: "Risk Factors"},
	{Name: "Item-1B", Number: "1B", Title: "Unresolved Staff Comments"},
	{Name: "Item-1C", Number: "1C", Title: "Cybersecurity"},
	{Name: "Item-2", Number: "2", Title: "Properties"},
	{Name: "Item-3", Number: "3", Title: "Legal Proceedings"},
	{Name: "Item-4", Number: "4", Title: "Mine Safety Disclosures"},
	{Name: "Item-5", Number: "5", Title: "Market for Registrant's Common Equity, Related Stockholder Matters and Issuer Purchases of Equity Securities"},
	{Name: "Item-6", Number: "6", Title: "Reserved"},
	{Name: "Item-7", Number: "7", Title: "Management's Discussion and Analysis of Financial Condition and Results of Operations"},
	{Name: "Item-7A", Number: "7A", Title: "Quantitative and Qualitative Disclosures About Market Risk"},
	{Name: "Item-8", Number: "8", Title: "Financial Statements and Supplementary Data"},
	{Name: "Item-9", Number: "9", Title: "Changes in and Disagreements with Accountants on Accounting and Financial Disclosure"},
	{Name: "Item-9A", Number: "9A", Title: "Controls and Procedures"},
	{Name: "Item-9B", Number: "9B", Title: "Other Information"},
	{Name: "Item-9C", Number: "9C", Title: "Disclosure Regarding Foreign Jurisdictions that Prevent Inspections"},
	{Name: "Item-10", Number: "10", Title: "Directors, Executive Officers and Corporate Governance"},
	{Name: "Item-11", Number: "11", Title: "Executive Compensation"},
	{Name: "Item-12", Number: "12", Title: "Security Ownership of Certain Beneficial Owners and Management and Related Stockholder Matters"},
	{Name: "Item-13", Number: "13", Title: "Certain Relationships and Related Transactions, and Director Independence"},
	{Name: "Item-14", Number: "14", Title: "Principal Accountant Fees and Services"},
	{Name: "Item-15", Number: "15", Title: "Exhibits and Financial Statement Schedules"},
	{Name: "Item-16", Number: "16", Title: "Form 10-K Summary"},
}

// TenQItems is the static SEC Form 10-Q item catalog (SEC 1296 (02-25)).
// Item numbers 1-4 repeat across Part I and Part II, so Name carries the
// part to disambiguate them (see ResolveItem).
var TenQItems = []FilingItem{
	{Name: "Part-I-Item-1", Number: "1", Title: "Financial Statements", Part: "I"},
	{Name: "Part-I-Item-2", Number: "2", Title: "Management's Discussion and Analysis of Financial Condition and Results of Operations", Part: "I"},
	{Name: "Part-I-Item-3", Number: "3", Title: "Quantitative and Qualitative Disclosures About Market Risk", Part: "I"},
	{Name: "Part-I-Item-4", Number: "4", Title: "Controls and Procedures", Part: "I"},
	{Name: "Part-II-Item-1", Number: "1", Title: "Legal Proceedings", Part: "II"},
	{Name: "Part-II-Item-1A", Number: "1A", Title: "Risk Factors", Part: "II"},
	{Name: "Part-II-Item-2", Number: "2", Title: "Unregistered Sales of Equity Securities and Use of Proceeds", Part: "II"},
	{Name: "Part-II-Item-3", Number: "3", Title: "Defaults Upon Senior Securities", Part: "II"},
	{Name: "Part-II-Item-4", Number: "4", Title: "Mine Safety Disclosures", Part: "II"},
	{Name: "Part-II-Item-5", Number: "5", Title: "Other Information", Part: "II"},
	{Name: "Part-II-Item-6", Number: "6", Title: "Exhibits", Part: "II"},
}

// EightKItems is the static SEC Form 8-K item catalog (SEC 873 (02-25)).
var EightKItems = []FilingItem{
	{Name: "Item-1.01", Number: "1.01", Title: "Entry into a Material Definitive Agreement"},
	{Name: "Item-1.02", Number: "1.02", Title: "Termination of a Material Definitive Agreement"},
	{Name: "Item-1.03", Number: "1.03", Title: "Bankruptcy or Receivership"},
	{Name: "Item-1.04", Number: "1.04", Title: "Mine Safety Reporting of Shutdowns and Patterns of Violations"},
	{Name: "Item-1.05", Number: "1.05", Title: "Material Cybersecurity Incidents"},
	{Name: "Item-2.01", Number: "2.01", Title: "Completion of Acquisition or Disposition of Assets"},
	{Name: "Item-2.02", Number: "2.02", Title: "Results of Operations and Financial Condition"},
	{Name: "Item-2.03", Number: "2.03", Title: "Creation of a Direct Financial Obligation or an Obligation under an Off-Balance Sheet Arrangement of a Registrant"},
	{Name: "Item-2.04", Number: "2.04", Title: "Triggering Events That Accelerate or Increase a Direct Financial Obligation or an Obligation under an Off-Balance Sheet Arrangement"},
	{Name: "Item-2.05", Number: "2.05", Title: "Costs Associated with Exit or Disposal Activities"},
	{Name: "Item-2.06", Number: "2.06", Title: "Material Impairments"},
	{Name: "Item-3.01", Number: "3.01", Title: "Notice of Delisting or Failure to Satisfy a Continued Listing Rule or Standard; Transfer of Listing"},
	{Name: "Item-3.02", Number: "3.02", Title: "Unregistered Sales of Equity Securities"},
	{Name: "Item-3.03", Number: "3.03", Title: "Material Modification to Rights of Security Holders"},
	{Name: "Item-4.01", Number: "4.01", Title: "Changes in Registrant's Certifying Accountant"},
	{Name: "Item-4.02", Number: "4.02", Title: "Non-Reliance on Previously Issued Financial Statements or a Related Audit Report or Completed Interim Review"},
	{Name: "Item-5.01", Number: "5.01", Title: "Changes in Control of Registrant"},
	{Name: "Item-5.02", Number: "5.02", Title: "Departure of Directors or Certain Officers; Election of Directors; Appointment of Certain Officers; Compensatory Arrangements of Certain Officers"},
	{Name: "Item-5.03", Number: "5.03", Title: "Amendments to Articles of Incorporation or Bylaws; Change in Fiscal Year"},
	{Name: "Item-5.04", Number: "5.04", Title: "Temporary Suspension of Trading Under Registrant's Employee Benefit Plans"},
	{Name: "Item-5.05", Number: "5.05", Title: "Amendments to the Registrant's Code of Ethics, or Waiver"},
	{Name: "Item-5.06", Number: "5.06", Title: "Change in Shell Company Status"},
	{Name: "Item-5.07", Number: "5.07", Title: "Submission of Matters to a Vote of Security Holders"},
	{Name: "Item-5.08", Number: "5.08", Title: "Shareholder Director Nominations"},
	{Name: "Item-6.01", Number: "6.01", Title: "ABS Informational and Computational Material"},
	{Name: "Item-6.02", Number: "6.02", Title: "Change of Servicer or Trustee"},
	{Name: "Item-6.03", Number: "6.03", Title: "Change in Credit Enhancement or Other External Support"},
	{Name: "Item-6.04", Number: "6.04", Title: "Failure to Make a Required Distribution"},
	{Name: "Item-6.05", Number: "6.05", Title: "Securities Act Updating Disclosure"},
	{Name: "Item-7.01", Number: "7.01", Title: "Regulation FD Disclosure"},
	{Name: "Item-8.01", Number: "8.01", Title: "Other Events"},
	{Name: "Item-9.01", Number: "9.01", Title: "Financial Statements and Exhibits"},
}

// Catalogs indexes the static SEC item catalogs by filing type, matching
// filing_items.py's CATALOGS. Catalog/revision identifiers are the exact SEC
// form-instruction identifiers: SEC-1673-02-25 (10-K), SEC-1296-02-25
// (10-Q), SEC-873-02-25 (8-K).
var Catalogs = map[string]FilingCatalog{
	"10-K": {
		FilingType:     "10-K",
		CatalogVersion: "SEC-1673-02-25",
		FormRevision:   "SEC 1673 (02-25)",
		SourceURL:      "https://www.sec.gov/files/form10-k.pdf",
		Items:          TenKItems,
	},
	"10-Q": {
		FilingType:     "10-Q",
		CatalogVersion: "SEC-1296-02-25",
		FormRevision:   "SEC 1296 (02-25)",
		SourceURL:      "https://www.sec.gov/files/form10-q.pdf",
		Items:          TenQItems,
	},
	"8-K": {
		FilingType:     "8-K",
		CatalogVersion: "SEC-873-02-25",
		FormRevision:   "SEC 873 (02-25)",
		SourceURL:      "https://www.sec.gov/files/form8-k.pdf",
		Items:          EightKItems,
	},
}

// InputError and SchemaDriftError (the tool-input and provider-schema-drift
// classification types the service layer maps to Financial Datasets error
// codes) are owned by errors.go; this file uses their newInputErrorf and
// schemaDriftf constructors rather than declaring its own.

// tickerRE mirrors providers/us/normalize.py's _TICKER. It is duplicated
// here (not imported) because that shared normalize module has not been
// ported to Go yet; once it lands, ValidateFilingItemRequest should call the
// shared helper instead of validateTickerSymbol.
var tickerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,19}$`)

func validateTickerSymbol(value string) (string, error) {
	ticker := strings.ToUpper(strings.TrimSpace(value))
	if !tickerRE.MatchString(ticker) {
		return "", newInputErrorf("ticker must be 1-20 letters, digits, dots, or hyphens")
	}
	return ticker, nil
}

var (
	// accessionFullRE requires the whole (trimmed) string to be one 18-digit
	// SEC accession number, with optional dashes after the 10th and 12th
	// digits, mirroring filing_items.py's _ACCESSION used with fullmatch:
	// anchored to the full string, its (?<!\d)/(?!\d) boundary guards are
	// redundant and dropped (Go's RE2 engine has no lookaround anyway). The
	// non-anchored search used for URLs (which does need those guards) is
	// DeriveAccession, owned by sec.go.
	accessionFullRE = regexp.MustCompile(`^(\d{10})-?(\d{2})-?(\d{6})$`)

	partHeadingRE = regexp.MustCompile(`(?i)^\s*(?:#{1,6}\s*)?(?:\*{1,2})?PART\s+(I{1,3}|IV)\b`)
	itemHeadingRE = regexp.MustCompile(`(?i)^\s*(?P<markdown>#{1,6}\s*)?(?P<bold>\*{1,2}|__)?(?:PART\s+(?P<part>I{1,3}|IV)\s*[-:.]?\s*)?ITEM\s+(?P<number>\d{1,2}(?:\.\d{2})?[A-Z]?)(?:\s*[.:-]\s*|\s+)(?P<title>.*)$`)

	markdownLinkRE  = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	markupRE        = regexp.MustCompile("[*_`#]")
	inlineXBRLTagRE = regexp.MustCompile(`(?i)</?ix:`)
	nonWordRE       = regexp.MustCompile(`[^A-Z0-9]+`)
	dotLeaderRE     = regexp.MustCompile(`(?:\.{2,}|…|\s\d{1,4}\s*$)`)
	htmlTagRE       = regexp.MustCompile(`<[^>]+>`)
)

// ValidateCatalogFilingType normalizes and validates a filing_type value
// against the static catalogs, mirroring validate_catalog_filing_type.
func ValidateCatalogFilingType(value string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if _, ok := Catalogs[normalized]; !ok {
		return "", newInputErrorf("filing_type must be 10-K, 10-Q, or 8-K")
	}
	return normalized, nil
}

// ValidateFilingItemRequest validates a get_filing_items request, mirroring
// validate_filing_item_request: the ticker, filing type, filing year bounds
// ([MinFilingYear, current year + 1]), the quarter bounds (1-4), and, if an
// item was requested, resolves it against the catalog (including the 10-Q
// Part I/Part II ambiguity rule in ResolveItem).
func ValidateFilingItemRequest(
	ticker, filingType string,
	year int,
	quarter *int,
	item *string,
) (symbol, normalizedType string, resolvedYear int, resolvedQuarter *int, selectedItem *FilingItem, err error) {
	symbol, err = validateTickerSymbol(ticker)
	if err != nil {
		return "", "", 0, nil, nil, err
	}
	normalizedType, err = ValidateCatalogFilingType(filingType)
	if err != nil {
		return "", "", 0, nil, nil, err
	}
	currentYear := time.Now().Year()
	if year < MinFilingYear || year > currentYear+1 {
		return "", "", 0, nil, nil, newInputErrorf("year must be between %d and %d", MinFilingYear, currentYear+1)
	}
	if quarter != nil && (*quarter < 1 || *quarter > 4) {
		return "", "", 0, nil, nil, newInputErrorf("quarter must be between 1 and 4")
	}
	if item != nil {
		resolved, resolveErr := ResolveItem(normalizedType, *item)
		if resolveErr != nil {
			return "", "", 0, nil, nil, resolveErr
		}
		selectedItem = &resolved
	}
	return symbol, normalizedType, year, quarter, selectedItem, nil
}

// aliasKey normalizes a value for alias matching by upper-casing it and
// stripping every non-alphanumeric character, mirroring _alias_key.
func aliasKey(value string) string {
	return nonWordRE.ReplaceAllString(strings.ToUpper(value), "")
}

// itemAliases returns every alias string that should resolve to item,
// mirroring _item_aliases: the item's own Name, "Item {number}", the bare
// number, and, for parted forms, several "Part {part} Item {number}"
// spellings.
func itemAliases(item FilingItem) map[string]struct{} {
	aliases := map[string]struct{}{
		aliasKey(item.Name):             {},
		aliasKey("Item " + item.Number): {},
		aliasKey(item.Number):           {},
	}
	if item.Part != "" {
		numericPart := "2"
		if item.Part == "I" {
			numericPart = "1"
		}
		aliases[aliasKey("Part "+item.Part+" Item "+item.Number)] = struct{}{}
		aliases[aliasKey(item.Part+" Item "+item.Number)] = struct{}{}
		aliases[aliasKey(item.Part+"-"+item.Number)] = struct{}{}
		aliases[aliasKey("Part-"+numericPart+",Item-"+item.Number)] = struct{}{}
	}
	return aliases
}

// ResolveItem resolves a user-supplied item name/number against filingType's
// catalog, mirroring resolve_item. A 10-Q item number that appears in both
// Part I and Part II (e.g. bare "Item-1") is ambiguous and returns an error
// naming the candidates; "Part I Item 1", "Part-1,Item-1", and
// "Part-II-Item-1" all resolve unambiguously.
func ResolveItem(filingType, value string) (FilingItem, error) {
	catalog, ok := Catalogs[filingType]
	if !ok {
		return FilingItem{}, newInputErrorf("filing_type must be 10-K, 10-Q, or 8-K")
	}
	key := aliasKey(value)
	var matches []FilingItem
	for _, item := range catalog.Items {
		if _, ok := itemAliases(item)[key]; ok {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.Name
		}
		return FilingItem{}, newInputErrorf(
			"item %q is ambiguous for %s; use one of: %s", value, filingType, strings.Join(names, ", "),
		)
	}
	allowed := make([]string, len(catalog.Items))
	for i, it := range catalog.Items {
		allowed[i] = it.Name
	}
	return FilingItem{}, newInputErrorf(
		"item must be one of the supported %s names: %s", filingType, strings.Join(allowed, ", "),
	)
}

// NormalizeAccession validates and normalizes an accession_number input to
// "NNNNNNNNNN-NN-NNNNNN", mirroring normalize_accession. A nil value returns
// (nil, nil); any non-nil value that is not exactly 18 digits (with optional
// SEC dashes) is an InputError.
func NormalizeAccession(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	m := accessionFullRE.FindStringSubmatch(trimmed)
	if m == nil {
		return nil, newInputErrorf("accession_number must contain 18 digits, with optional SEC dashes")
	}
	joined := strings.Join(m[1:], "-")
	return &joined, nil
}

// ValidateSECURL validates that value is an HTTPS sec.gov/www.sec.gov URL
// under /Archives/ that contains an accession number, mirroring
// validate_sec_url. It returns a plain error (not a typed
// InputError/SchemaDriftError), matching Python's bare ValueError; callers
// choose how to map it (service.py maps it to upstream_error or wraps it
// into a SchemaDriftError depending on call site). DeriveAccession, used
// below to check for an accession number, is owned by sec.go.
func ValidateSECURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("selected filing URL is malformed")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	portOK := port == "" || port == "443"
	if !strings.EqualFold(parsed.Scheme, "https") ||
		(host != "sec.gov" && host != "www.sec.gov") ||
		parsed.User != nil ||
		!portOK ||
		!strings.HasPrefix(parsed.Path, "/Archives/") {
		return "", fmt.Errorf("selected filing URL must use HTTPS on sec.gov and point under /Archives/")
	}
	if DeriveAccession(value) == nil {
		return "", fmt.Errorf("selected SEC filing URL does not contain an accession number")
	}
	return value, nil
}

// SelectedFiling is one matched SEC filing row, mirroring
// filing_items.py's SelectedFiling dataclass.
type SelectedFiling struct {
	FilingDate          string
	ReportDate          string
	Form                string
	DocumentDescription *string
	SourceURL           string
	AccessionNumber     string
}

// ToDict renders the filing the way SelectedFiling.to_dict does.
func (f SelectedFiling) ToDict() map[string]any {
	var description any
	if f.DocumentDescription != nil {
		description = *f.DocumentDescription
	}
	return map[string]any{
		"filing_date":          f.FilingDate,
		"report_date":          f.ReportDate,
		"form":                 f.Form,
		"document_description": description,
		"source_url":           f.SourceURL,
		"accession_number":     f.AccessionNumber,
	}
}

// FilingSelection is the result of SelectFiling: the newest matching filing
// (or nil) plus how many filings matched, mirroring FilingSelection.
type FilingSelection struct {
	Filing        *SelectedFiling
	MatchingCount int
}

// asListOrMap type-asserts value as either a JSON array ([]any) or object
// (map[string]any), returning ok=false for anything else.
func asListOrMap(value any) (any, bool) {
	switch value.(type) {
	case []any, map[string]any:
		return value, true
	default:
		return nil, false
	}
}

// filingRecords descends into a raw provider payload to find the list of
// filing rows, mirroring _filing_records: it walks up to four levels
// through "data" and "filings" wrapper keys before giving up.
func filingRecords(value any) ([]map[string]any, error) {
	current := value
	for i := 0; i < 4; i++ {
		if list, ok := current.([]any); ok {
			records := make([]map[string]any, 0, len(list))
			for index, item := range list {
				obj, ok := item.(map[string]any)
				if !ok {
					return nil, schemaDriftf("DefiLlama filing row %d is not an object", index)
				}
				records = append(records, obj)
			}
			return records, nil
		}
		obj, ok := current.(map[string]any)
		if !ok {
			break
		}
		if child, ok := asListOrMap(obj["data"]); ok {
			current = child
			continue
		}
		if child, ok := asListOrMap(obj["filings"]); ok {
			current = child
			continue
		}
		break
	}
	return nil, schemaDriftf("DefiLlama payload omitted filing records")
}

// isoDate parses the first 10 characters of value as a YYYY-MM-DD date,
// mirroring _iso_date. It returns nil for anything that is not a string or
// does not parse.
func isoDate(value any) *time.Time {
	s, ok := value.(string)
	if !ok || len(s) < 10 {
		return nil
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return nil
	}
	return &t
}

// optionalString returns value as a string pointer when value is a non-blank
// string, mirroring _optional_string; the returned string is not trimmed.
func optionalString(value any) *string {
	s, ok := value.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

type selectionCandidate struct {
	filingDate time.Time
	reportDate time.Time
	accession  string
	sourceURL  string
	selected   SelectedFiling
}

// compareCandidates orders two candidates the way select_filing's sort key
// (filing_date, report_date, accession, source_url) does: it returns a
// positive number when a sorts after b (a is "greater"/newer).
func compareCandidates(a, b selectionCandidate) int {
	if !a.filingDate.Equal(b.filingDate) {
		if a.filingDate.After(b.filingDate) {
			return 1
		}
		return -1
	}
	if !a.reportDate.Equal(b.reportDate) {
		if a.reportDate.After(b.reportDate) {
			return 1
		}
		return -1
	}
	if a.accession != b.accession {
		if a.accession > b.accession {
			return 1
		}
		return -1
	}
	if a.sourceURL != b.sourceURL {
		if a.sourceURL > b.sourceURL {
			return 1
		}
		return -1
	}
	return 0
}

// SelectFiling deterministically picks the newest filing matching
// filingType, year, and (optionally) quarter and accessionNumber, mirroring
// select_filing. value is the raw (already JSON-decoded) provider payload
// from the filings endpoint. Filings are ordered newest-first by
// (filing_date, report_date, accession_number, source_url); ties are broken
// by that same descending order, so the choice is fully deterministic.
func SelectFiling(
	value any,
	filingType string,
	year int,
	quarter *int,
	accessionNumber *string,
) (FilingSelection, error) {
	records, err := filingRecords(value)
	if err != nil {
		return FilingSelection{}, err
	}
	var candidates []selectionCandidate
	for _, record := range records {
		form := optionalString(record["form"])
		if form == nil || strings.ToUpper(strings.TrimSpace(*form)) != filingType {
			continue
		}
		reportDate := isoDate(record["reportDate"])
		filingDate := isoDate(record["filingDate"])
		if reportDate == nil || filingDate == nil || reportDate.Year() != year {
			continue
		}
		if quarter != nil {
			q := ((int(reportDate.Month()) - 1) / 3) + 1
			if q != *quarter {
				continue
			}
		}
		sourceURL := optionalString(record["primaryDocumentUrl"])
		if sourceURL == nil {
			continue
		}
		accessionPtr := DeriveAccession(*sourceURL)
		if accessionNumber != nil && (accessionPtr == nil || *accessionPtr != *accessionNumber) {
			continue
		}
		accession := ""
		if accessionPtr != nil {
			accession = *accessionPtr
		}
		description := optionalString(record["documentDescription"])
		selected := SelectedFiling{
			FilingDate:          filingDate.Format("2006-01-02"),
			ReportDate:          reportDate.Format("2006-01-02"),
			Form:                strings.ToUpper(strings.TrimSpace(*form)),
			DocumentDescription: description,
			SourceURL:           *sourceURL,
			AccessionNumber:     accession,
		}
		candidates = append(candidates, selectionCandidate{
			filingDate: *filingDate,
			reportDate: *reportDate,
			accession:  accession,
			sourceURL:  *sourceURL,
			selected:   selected,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return compareCandidates(candidates[i], candidates[j]) > 0
	})
	if len(candidates) == 0 {
		return FilingSelection{Filing: nil, MatchingCount: 0}, nil
	}
	top := candidates[0].selected
	return FilingSelection{Filing: &top, MatchingCount: len(candidates)}, nil
}

// numberAsInt reports whether value is a JSON number with no fractional
// part (and not a bool, which Go's encoding/json would otherwise treat as a
// non-numeric type anyway but Python must exclude explicitly since bool is
// an int subclass), mirroring the isinstance(x, bool)/isinstance(x, int)
// guard in parse_scrape_payload.
func numberAsInt(value any) (int, bool) {
	switch v := value.(type) {
	case bool:
		return 0, false
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

// ParseScrapePayload validates and unwraps a Context.dev /web/scrape/markdown
// run output, mirroring parse_scrape_payload: it requires an object (optionally
// nested under "data") with success=true, non-blank markdown, a non-negative
// integer contentLength, and a returned url that is both a safe SEC URL and
// exactly the expectedURL. It returns the markdown and a metadata map with
// content_length/url/metadata/cache_metadata.
func ParseScrapePayload(value any, expectedURL string) (string, map[string]any, error) {
	payload := value
	if m, ok := value.(map[string]any); ok {
		if data, ok := m["data"].(map[string]any); ok {
			payload = data
		}
	}
	obj, ok := payload.(map[string]any)
	if !ok {
		return "", nil, schemaDriftf("Context.dev scrape payload is not an object")
	}
	if success, ok := obj["success"].(bool); !ok || !success {
		return "", nil, schemaDriftf("Context.dev scrape did not report success")
	}
	markdown, ok := obj["markdown"].(string)
	if !ok || strings.TrimSpace(markdown) == "" {
		return "", nil, schemaDriftf("Context.dev scrape returned empty markdown")
	}
	contentLength, ok := numberAsInt(obj["contentLength"])
	if !ok || contentLength < 0 {
		return "", nil, schemaDriftf("Context.dev scrape contentLength must be a non-negative integer")
	}
	returnedURL, ok := obj["url"].(string)
	if !ok {
		return "", nil, schemaDriftf("Context.dev scrape omitted its source URL")
	}
	if _, err := ValidateSECURL(returnedURL); err != nil {
		return "", nil, schemaDriftf("Context.dev scrape returned an unsafe source URL: %s", err.Error())
	}
	if returnedURL != expectedURL {
		return "", nil, schemaDriftf("Context.dev scrape returned a different SEC filing URL")
	}
	meta := map[string]any{
		"content_length": contentLength,
		"url":            returnedURL,
		"metadata":       obj["metadata"],
		"cache_metadata": obj["cache_metadata"],
	}
	return markdown, meta, nil
}

// heading is one located, catalog-matched item heading within a markdown
// document, mirroring _Heading.
type heading struct {
	item         FilingItem
	start        int
	bodyStart    int
	titleScore   int
	looksLikeTOC bool
}

// splitLinesKeepEnds splits s into lines the way Python's
// str.splitlines(keepends=True) does for \n, \r\n, and \r line endings
// (each terminator stays attached to the line it ends). Python's splitlines
// also treats several rarer separators (\v, \f, \x1c-\x1e, U+0085, U+2028,
// U+2029) as line breaks; scraped filing markdown does not use those, so
// this port intentionally covers only the three ordinary ASCII endings.
func splitLinesKeepEnds(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '\n':
			lines = append(lines, s[start:i+1])
			i++
			start = i
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				lines = append(lines, s[start:i+2])
				i += 2
			} else {
				lines = append(lines, s[start:i+1])
				i++
			}
			start = i
		default:
			i++
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// cleanHeadingLine strips markdown links (keeping their label text) and any
// HTML tags, then trims whitespace, mirroring _clean_heading_line.
func cleanHeadingLine(value string) string {
	linked := markdownLinkRE.ReplaceAllString(value, "$1")
	return strings.TrimSpace(htmlTagRE.ReplaceAllString(linked, ""))
}

// lettersAreUpper reports whether every letter in value is uppercase (and
// value contains at least one letter), mirroring _letters_are_upper.
func lettersAreUpper(value string) bool {
	sawLetter := false
	for _, r := range value {
		if unicode.IsLetter(r) {
			sawLetter = true
			if !unicode.IsUpper(r) {
				return false
			}
		}
	}
	return sawLetter
}

// wordSet splits value (already upper-cased) on runs of non-alphanumeric
// characters into a set of words, mirroring the set(...split()) idiom shared
// by _title_score's actual_words/expected_words.
func wordSet(upperValue string) map[string]struct{} {
	spaced := nonWordRE.ReplaceAllString(upperValue, " ")
	words := map[string]struct{}{}
	for _, w := range strings.Fields(spaced) {
		words[w] = struct{}{}
	}
	return words
}

// titleScore scores how well actual (an item heading's captured title text)
// matches expected (the catalog's canonical title), mirroring _title_score:
// 100 * (shared words) / (words in expected), truncated to an int, or 0 if
// actual has no words at all.
func titleScore(actual, expected string) int {
	actualWords := wordSet(strings.ToUpper(markupRE.ReplaceAllString(actual, "")))
	if len(actualWords) == 0 {
		return 0
	}
	expectedWords := wordSet(strings.ToUpper(expected))
	shared := 0
	for w := range actualWords {
		if _, ok := expectedWords[w]; ok {
			shared++
		}
	}
	denominator := len(expectedWords)
	if denominator < 1 {
		denominator = 1
	}
	return int(100 * float64(shared) / float64(denominator))
}

// namedGroups maps itemHeadingRE's named capture groups to their matched
// text for one FindStringSubmatch result (missing/unmatched groups map to
// "").
func namedGroups(re *regexp.Regexp, match []string) map[string]string {
	groups := make(map[string]string, len(match))
	for i, name := range re.SubexpNames() {
		if i == 0 || name == "" || i >= len(match) {
			continue
		}
		groups[name] = match[i]
	}
	return groups
}

// findHeadings scans markdown line by line for item headings that resolve to
// one of catalog's items, mirroring _find_headings. It skips lines carrying
// inline XBRL tags (<ix:.../>) as noise, tracks the current SEC Part from
// "PART I"/"PART II" lines, and tracks a "Table of Contents" run so that
// headings appearing inside it are flagged looksLikeTOC (the TOC run ends
// once a part or item name it already listed repeats, i.e. the real body
// section for that heading has begun).
func findHeadings(markdown string, catalog FilingCatalog) []heading {
	type lookupKey struct{ part, number string }
	lookup := make(map[lookupKey]FilingItem, len(catalog.Items))
	numberCounts := map[string]int{}
	for _, item := range catalog.Items {
		lookup[lookupKey{item.Part, item.Number}] = item
		numberCounts[item.Number]++
	}

	var headings []heading
	currentPart := ""
	tocActive := false
	tocItems := map[string]struct{}{}
	tocParts := map[string]struct{}{}
	offset := 0

	for _, rawLine := range splitLinesKeepEnds(markdown) {
		line := strings.TrimRight(rawLine, "\r\n")
		if inlineXBRLTagRE.MatchString(line) {
			offset += len(rawLine)
			continue
		}
		clean := cleanHeadingLine(line)
		if aliasKey(clean) == "TABLEOFCONTENTS" {
			tocActive = true
			tocItems = map[string]struct{}{}
			tocParts = map[string]struct{}{}
		}
		if m := partHeadingRE.FindStringSubmatch(clean); m != nil {
			matchedPart := strings.ToUpper(m[1])
			currentPart = matchedPart
			if tocActive {
				if _, seen := tocParts[matchedPart]; seen {
					tocActive = false
				} else {
					tocParts[matchedPart] = struct{}{}
				}
			}
		}
		if m := itemHeadingRE.FindStringSubmatch(clean); m != nil {
			groups := namedGroups(itemHeadingRE, m)
			number := strings.ToUpper(groups["number"])
			part := currentPart
			if explicitPart := groups["part"]; explicitPart != "" {
				part = strings.ToUpper(explicitPart)
			}
			item, ok := lookup[lookupKey{part, number}]
			if !ok && numberCounts[number] == 1 {
				for _, candidate := range catalog.Items {
					if candidate.Number == number {
						item = candidate
						ok = true
						break
					}
				}
			}
			if ok {
				if tocActive {
					if _, seen := tocItems[item.Name]; seen {
						tocActive = false
					}
				}
				title := strings.TrimSpace(groups["title"])
				score := titleScore(title, item.Title)
				hasHeadingMarkup := groups["markdown"] != "" || groups["bold"] != ""
				labelIsUpper := lettersAreUpper(clean)
				if score > 0 || hasHeadingMarkup || labelIsUpper {
					headings = append(headings, heading{
						item:         item,
						start:        offset,
						bodyStart:    offset + len(rawLine),
						titleScore:   score,
						looksLikeTOC: tocActive || dotLeaderRE.MatchString(title),
					})
					if tocActive {
						tocItems[item.Name] = struct{}{}
					}
				}
			}
		}
		offset += len(rawLine)
	}
	return headings
}

// sectionEnd returns the offset where h's section ends: the start of the
// nearest later heading, or documentEnd if none follows, mirroring
// _section_end.
func sectionEnd(h heading, headings []heading, documentEnd int) int {
	end := documentEnd
	for _, candidate := range headings {
		if candidate.start > h.start && candidate.start < end {
			end = candidate.start
		}
	}
	return end
}

// bestHeading picks the best-supported heading for one item among its
// candidates, mirroring _best_heading. Candidates flagged looksLikeTOC (a
// table-of-contents entry or a dot-leader/page-number line) are excluded
// outright, so a body heading is always preferred over a TOC duplicate. Among
// the rest, it ranks by (has >= 80 chars of content, earliest position,
// content length capped at 200,000, title match score) and returns the
// highest-ranked one, keeping the first heading encountered on ties (Go's
// analogue of Python's max() tie-breaking).
func bestHeading(markdown string, candidates, headings []heading) *heading {
	type rankedCandidate struct {
		key [4]int
		h   heading
	}
	var ranked []rankedCandidate
	for _, c := range candidates {
		if c.looksLikeTOC {
			continue
		}
		end := sectionEnd(c, headings, len(markdown))
		contentLength := len(strings.TrimSpace(markdown[c.bodyStart:end]))
		if contentLength == 0 {
			continue
		}
		cappedLength := contentLength
		if cappedLength > 200_000 {
			cappedLength = 200_000
		}
		atLeast80 := 0
		if contentLength >= 80 {
			atLeast80 = 1
		}
		ranked = append(ranked, rankedCandidate{key: [4]int{atLeast80, c.start, cappedLength, c.titleScore}, h: c})
	}
	if len(ranked) == 0 {
		return nil
	}
	best := ranked[0]
	for _, candidate := range ranked[1:] {
		if compareRankKey(candidate.key, best.key) > 0 {
			best = candidate
		}
	}
	result := best.h
	return &result
}

func compareRankKey(a, b [4]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ParseFilingSections extracts every catalog section it can find in
// markdown (or just requestedItem's section, if given), mirroring
// parse_filing_sections. Sections are returned in catalog order (not
// document order) as maps with "item"/"number"/"part"/"title"/"content"
// keys. A candidate heading is only accepted once its section has non-blank
// content, and table-of-contents spans are excluded in favor of the real
// body span (see bestHeading).
func ParseFilingSections(markdown, filingType string, requestedItem *FilingItem) ([]map[string]any, error) {
	if strings.TrimSpace(markdown) == "" {
		return nil, schemaDriftf("Context.dev scrape returned empty markdown")
	}
	catalog, ok := Catalogs[filingType]
	if !ok {
		return nil, newInputErrorf("filing_type must be 10-K, 10-Q, or 8-K")
	}
	headings := findHeadings(markdown, catalog)
	wanted := catalog.Items
	if requestedItem != nil {
		wanted = []FilingItem{*requestedItem}
	}
	var sections []map[string]any
	for _, item := range wanted {
		var candidates []heading
		for _, h := range headings {
			if h.item == item {
				candidates = append(candidates, h)
			}
		}
		best := bestHeading(markdown, candidates, headings)
		if best == nil {
			continue
		}
		end := sectionEnd(*best, headings, len(markdown))
		content := strings.TrimSpace(markdown[best.bodyStart:end])
		if content == "" {
			continue
		}
		var part any
		if item.Part != "" {
			part = item.Part
		}
		sections = append(sections, map[string]any{
			"item":    item.Name,
			"number":  item.Number,
			"part":    part,
			"title":   item.Title,
			"content": content,
		})
	}
	return sections, nil
}

// CatalogPayload renders one or all static SEC item catalogs, mirroring
// catalog_payload. A nil filingType returns all three catalogs in
// 10-K/10-Q/8-K order.
func CatalogPayload(filingType *string) (map[string]any, error) {
	var selected []FilingCatalog
	if filingType != nil {
		catalog, ok := Catalogs[*filingType]
		if !ok {
			return nil, newInputErrorf("filing_type must be 10-K, 10-Q, or 8-K")
		}
		selected = []FilingCatalog{catalog}
	} else {
		for _, t := range []string{"10-K", "10-Q", "8-K"} {
			selected = append(selected, Catalogs[t])
		}
	}
	catalogs := make([]map[string]any, len(selected))
	for i, c := range selected {
		catalogs[i] = c.ToDict()
	}
	return map[string]any{
		"catalogs":      catalogs,
		"catalog_scope": CatalogScope,
	}, nil
}

// FilingItemTypeEntry is one row of the list_filing_item_types catalog
// response. Description duplicates Title: the static SEC catalogs have no
// separate description field, matching service.py's
// list_filing_item_types.
type FilingItemTypeEntry struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// ListFilingItemTypes renders the list_filing_item_types catalog response,
// keyed "10-K"/"10-Q"/"8-K" -> its item entries, mirroring
// FinanceService.list_filing_item_types. A nil filingType keys all three
// forms; a non-nil one must name exactly one of them.
func ListFilingItemTypes(filingType *string) (map[string][]FilingItemTypeEntry, error) {
	types := []string{"10-K", "10-Q", "8-K"}
	if filingType != nil {
		normalized, err := ValidateCatalogFilingType(*filingType)
		if err != nil {
			return nil, err
		}
		types = []string{normalized}
	}
	response := make(map[string][]FilingItemTypeEntry, len(types))
	for _, t := range types {
		catalog := Catalogs[t]
		entries := make([]FilingItemTypeEntry, len(catalog.Items))
		for i, item := range catalog.Items {
			entries[i] = FilingItemTypeEntry{Name: item.Name, Title: item.Title, Description: item.Title}
		}
		response[catalog.FilingType] = entries
	}
	return response, nil
}

// FilingItemRecord builds one item entry for the get_filing_items response,
// reusing fd.FilingItem's field order (number, name, text, exhibits),
// mirroring fd.py's filing_item_record. Exhibits is always left nil/omitted:
// the validated route cannot identify or fetch filing exhibits (see
// include_exhibits in service.py's get_filing_items), so it is never
// fabricated.
func FilingItemRecord(number, name, text string) fd.FilingItem {
	return fd.FilingItem{Number: &number, Name: &name, Text: &text}
}

// FilingItemsResponse is the get_filing_items response envelope, mirroring
// fd.py's filing_items_response. Unlike fd package record types (pointer +
// omitempty fields for "maybe sourced from upstream"), every field here is
// always present: this is a service-built envelope, not an upstream record,
// and e.g. Quarter is genuinely null for a 10-K rather than merely
// unsourced. The broader FD FilingItemsResponse contract also lists an
// optional top-level "cik" field; the current pipeline has no sourced CIK
// for a filing_items call and the Python implementation never emits that
// key at all (see fd.py's filing_items_response), so it is intentionally
// absent here too rather than fabricated.
type FilingItemsResponse struct {
	Resource        string          `json:"resource"`
	Ticker          string          `json:"ticker"`
	CIK             *string         `json:"cik,omitempty"`
	FilingType      string          `json:"filing_type"`
	AccessionNumber *string         `json:"accession_number"`
	Year            int             `json:"year"`
	Quarter         *int            `json:"quarter,omitempty"`
	FilingURL       string          `json:"filing_url"`
	Items           []fd.FilingItem `json:"items"`
}

// BuildFilingItemsResponse assembles the get_filing_items response envelope,
// mirroring fd.py's filing_items_response. accessionNumber and quarter are
// pointers so the response can carry an explicit JSON null (an empty/absent
// accession, or a 10-K's null quarter) rather than an empty string or 0.
func BuildFilingItemsResponse(
	resource, ticker, filingType string,
	accessionNumber *string,
	year int,
	quarter *int,
	items []fd.FilingItem,
	cik *string,
) FilingItemsResponse {
	if items == nil {
		items = []fd.FilingItem{}
	}
	return FilingItemsResponse{
		Resource:        "filing_items",
		Ticker:          ticker,
		CIK:             cik,
		FilingType:      filingType,
		AccessionNumber: accessionNumber,
		Year:            year,
		Quarter:         quarter,
		FilingURL:       resource,
		Items:           items,
	}
}
