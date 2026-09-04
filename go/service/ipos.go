// This file builds getIPOs, the /ipos route, from SEC registration
// statements in the same defillama filings feed get_filings already reads.
//
// Financial Datasets' /ipos returns S-1 and S-1/A filings, not a forward
// calendar. This port serves the same thing from the same source, so the
// records are genuine Ipo records rather than a differently-shaped
// substitute. Verified live 2026-09-04: the filings feed for RDDT carries
// S-1 alongside 8-K, 10-Q, 10-K, DEF 14A and 424B4.
//
// Scope, and what this route cannot do. The feed is keyed on one ticker,
// so this route requires one. Financial Datasets answers /ipos unscoped
// with the latest registrations market-wide; that needs a
// registrations-by-date feed no allowlisted provider offers, so an
// omitted ticker is a bad_request naming the reason rather than a guess.
//
// Fields that need the S-1's own contents (classification, is_ipo_grade,
// exchange, expected_offering_price, price_range_low/high) are declared
// by the Financial Datasets Ipo contract but never sourced here: reading
// them means parsing the registration statement's cover page, which this
// route does not do. They are omitted rather than guessed. The
// classification query parameter is rejected for the same reason: this
// server cannot classify a filing, so silently ignoring the filter would
// return a set the caller did not ask for.
//
// getIPOCalendar (ipocalendar.go) is a different thing and stays
// separate: stockanalysis's forward calendar of upcoming offerings, which
// answers "what is about to list", not "what registered".
package service

import (
	"sort"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

// ipoFormTypes are the SEC registration forms this route reports, matching
// the Financial Datasets Ipo.form_type enum.
var ipoFormTypes = map[string]bool{"S-1": true, "S-1/A": true}

// ipoRecord is one registration filing, field-named and ordered after the
// Financial Datasets Ipo contract as far as the filings feed can source
// it (see the file doc comment for the fields it cannot).
type ipoFilingRecord struct {
	AccessionNumber *string `json:"accession_number,omitempty"`
	CIK             *string `json:"cik,omitempty"`
	FormType        *string `json:"form_type,omitempty"`
	FilingDate      *string `json:"filing_date,omitempty"`
	FilingURL       *string `json:"filing_url,omitempty"`
	Ticker          *string `json:"ticker,omitempty"`
}

// getIPOs answers get_ipos: the S-1 and S-1/A registration statements one
// issuer has filed, newest first.
func (c *callCtx) getIPOs(args map[string]any) (Result, error) {
	if classification, err := argString(args, "classification"); err != nil {
		return Result{}, err
	} else if classification != nil {
		return Result{Value: fd.NewErrorResponse("bad_request",
			"classification is not supported; this server reports the registration filings SEC "+
				"publishes and does not classify them as ipo, spac, shell_company or resale")}, nil
	}

	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required: the filings feed behind this route is " +
			"keyed on one issuer and cannot list registrations across the whole market"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	filingDate, err := parseDateFilterGroup(args, "filing_date")
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 10)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 1000)
	if err != nil {
		return Result{}, err
	}

	run, err := c.run(defillama, filingsEndpoint, nil, map[string]any{"ticker": symbol})
	if err != nil {
		return Result{}, err
	}
	filings, err := providers.NormalizeFilings(run.Output, symbol, nil, 10_000, nil, nil)
	if err != nil {
		return Result{}, err
	}

	records := make([]ipoFilingRecord, 0, 8)
	for _, f := range filings {
		if f.FilingType == nil || !ipoFormTypes[strings.ToUpper(*f.FilingType)] {
			continue
		}
		if filingDate.any() {
			if f.FilingDate == nil {
				continue
			}
			day, perr := time.Parse(dateLayout, *f.FilingDate)
			if perr != nil || !filingDate.matches(day) {
				continue
			}
		}
		form := strings.ToUpper(*f.FilingType)
		rec := ipoFilingRecord{
			AccessionNumber: f.AccessionNumber,
			FormType:        &form,
			FilingDate:      f.FilingDate,
			FilingURL:       f.URL,
			Ticker:          f.Ticker,
		}
		if f.URL != nil {
			rec.CIK = cikFromEdgarURL(*f.URL)
		}
		records = append(records, rec)
	}
	sort.SliceStable(records, func(i, j int) bool {
		return ipoFilingDate(records[i]) > ipoFilingDate(records[j])
	})
	if len(records) > limit {
		records = records[:limit]
	}

	out := make([]any, len(records))
	for i, r := range records {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "ipos", Paginate: true}, nil
}

func ipoFilingDate(r ipoFilingRecord) string {
	if r.FilingDate != nil {
		return *r.FilingDate
	}
	return ""
}
