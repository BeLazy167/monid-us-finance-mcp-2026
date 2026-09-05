// Multi-path routing.
//
// Monid reaches many providers, and more than one of them can answer the
// same question. A route that names a single provider turns that
// provider's bad day into an empty response: the 13D/13G feed behind
// beneficial ownership, for instance, is a four-day window that a given
// issuer is usually absent from, so a ticker query against it returned
// nothing at all. The routes here try their sources in order and take the
// first that actually carries rows.
package service

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

// firstNonEmpty runs each source in order and returns the first that
// yields rows. A source that errors is recorded and skipped, so one dead
// provider cannot mask a live one; the error is only returned when every
// source failed. All sources returning zero rows is not an error, it is an
// honest empty answer.
func firstNonEmpty[T any](sources ...func() ([]T, error)) ([]T, error) {
	var firstErr error
	failed := 0
	for _, source := range sources {
		rows, err := source()
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(rows) > 0 {
			return rows, nil
		}
	}
	if failed == len(sources) && firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

// ---- EDGAR as a second path to 13D/13G stakes ----

// edgarSC13URL lists the Schedule 13D and 13G filings naming one issuer as
// the subject company, newest first. EDGAR indexes these under the issuer
// as well as the filer, which is what makes a per-ticker answer possible.
func edgarSC13URL(cik string) string {
	return fmt.Sprintf("https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=%s"+
		"&type=SC+13&dateb=&owner=include&count=40&output=atom", cik)
}

var (
	edgarEntryRE = regexp.MustCompile(`(?s)<entry>(.*?)</entry>`)
	edgarFieldRE = map[string]*regexp.Regexp{
		"date":      regexp.MustCompile(`(?s)<filing-date>(.*?)</filing-date>`),
		"type":      regexp.MustCompile(`(?s)<filing-type>(.*?)</filing-type>`),
		"accession": regexp.MustCompile(`(?s)<accession-number>(.*?)</accession-number>`),
		"href":      regexp.MustCompile(`(?s)<filing-href>(.*?)</filing-href>`),
	}
)

// edgarSC13Filing is one Schedule 13 filing as EDGAR's index lists it.
// The holder, the size of the stake and the event date live inside the
// filing itself, not in this index.
type edgarSC13Filing struct {
	FormType   string
	FilingDate string
	Accession  string
	IndexURL   string
}

// parseEdgarSC13 reads the Atom index into filings, newest first.
func parseEdgarSC13(atom string) []edgarSC13Filing {
	var out []edgarSC13Filing
	for _, entry := range edgarEntryRE.FindAllStringSubmatch(atom, -1) {
		field := func(name string) string {
			m := edgarFieldRE[name].FindStringSubmatch(entry[1])
			if m == nil {
				return ""
			}
			return strings.TrimSpace(m[1])
		}
		filing := edgarSC13Filing{
			FormType:   field("type"),
			FilingDate: field("date"),
			Accession:  field("accession"),
			IndexURL:   field("href"),
		}
		if filing.FormType == "" || !strings.HasPrefix(filing.FormType, "SC 13") {
			continue
		}
		out = append(out, filing)
	}
	return out
}

// sc13ExtractSchema asks for the fields the filing states and this server
// otherwise has no source for.
var sc13ExtractSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"reporting_person_name":     map[string]any{"type": []any{"string", "null"}},
		"shares_beneficially_owned": map[string]any{"type": []any{"number", "null"}},
		"percent_of_class":          map[string]any{"type": []any{"number", "null"}},
		"event_date":                map[string]any{"type": []any{"string", "null"}},
	},
	"required": []string{"reporting_person_name", "shares_beneficially_owned", "percent_of_class", "event_date"},
}

const sc13ExtractInstructions = "This is an SEC Schedule 13D or 13G beneficial ownership filing. " +
	"Return the reporting person or institution that filed it, the aggregate number of shares they " +
	"report owning beneficially, the percent of the class that represents, and the date of the event " +
	"that required the filing, as an ISO date. Use only figures stated in the filing; return null for " +
	"anything it does not state."

// beneficialFromEDGAR is the second path to a ticker's 5% holders: the
// issuer's own Schedule 13 index, then one extraction per filing for the
// holder and the size of the stake. It costs one scrape plus one
// extraction per row, so limit bounds how many filings are opened.
func (c *callCtx) beneficialFromEDGAR(symbol, formType string, limit int) ([]beneficialOwnershipRow, error) {
	cik, found, err := c.resolveIssuerCIK(symbol)
	if err != nil || !found || cik == "" {
		return nil, err
	}
	run, err := c.run(contextDev, scrapeHTMLEndpoint, nil, map[string]any{"url": edgarSC13URL(cik)})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success bool   `json:"success"`
		HTML    string `json:"html"`
	}
	if jerr := json.Unmarshal(run.Output, &envelope); jerr != nil {
		return nil, &providers.SchemaDriftError{Msg: "context.dev scrape payload must be an object"}
	}
	if !envelope.Success || envelope.HTML == "" {
		return nil, &providers.SchemaDriftError{Msg: "context.dev returned no content for the EDGAR Schedule 13 index"}
	}
	atom := envelope.HTML
	filings := parseEdgarSC13(atom)
	if formType != "" {
		kept := filings[:0]
		for _, f := range filings {
			if strings.EqualFold(strings.TrimSuffix(f.FormType, "/A"), formType) {
				kept = append(kept, f)
			}
		}
		filings = kept
	}
	if limit > 0 && len(filings) > limit {
		filings = filings[:limit]
	}

	rows := make([]beneficialOwnershipRow, 0, len(filings))
	for _, f := range filings {
		row := beneficialOwnershipRow{}
		ticker := symbol
		row.Ticker = &ticker
		form := f.FormType
		row.FormType = &form
		stake := "passive"
		if strings.HasPrefix(form, "SC 13D") {
			stake = "activist"
		}
		row.Type = &stake
		if f.FilingDate != "" {
			day := f.FilingDate
			row.FilingDate = &day
		}
		if data, derr := c.extractSC13(f.IndexURL); derr == nil && data != nil {
			if name, ok := data["reporting_person_name"].(string); ok && name != "" {
				row.ReportingPersonName = &name
			}
			if shares, ok := data["shares_beneficially_owned"].(float64); ok {
				row.AggregateAmountBeneficiallyOwned = &shares
			}
			if pct, ok := data["percent_of_class"].(float64); ok {
				row.PercentOfClass = &pct
			}
			if event, ok := data["event_date"].(string); ok {
				if day := parseOptDate(event); day != nil {
					iso := day.Format(dateLayout)
					row.EventDate = &iso
				}
			}
		}
		// A row with no holder is an index line, not a stake.
		if row.ReportingPersonName == nil {
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// extractSC13 reads one Schedule 13 filing for the fields its index omits.
func (c *callCtx) extractSC13(indexURL string) (map[string]any, error) {
	run, err := c.run(contextDev, extractEndpoint, extractRequestBody(indexURL, sc13ExtractSchema, sc13ExtractInstructions), nil)
	if err != nil {
		return nil, err
	}
	return parseExtractOutput(run.Output)
}
