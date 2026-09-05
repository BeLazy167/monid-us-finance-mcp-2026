// This file builds get_beneficial_owners and get_beneficial_ownership from
// SECForm4's /get_13d_filings (Schedule 13D, activist) and
// /get_13g_filings (Schedule 13G, passive) routes: both return every
// recent 5%+ stake across issuers, not one issuer's stakes, so both tools
// scan the same two feeds rather than querying per ticker.
//
// Freshness: the newest record in this feed was measured live at
// 2026-03-06, roughly six months behind the date measured (see
// docs/compatibility.md). Every row below keeps its own filing/event date
// so a caller can judge recency; this file never claims the data is
// current.
package service

import (
	"sort"
	"strings"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/monid"
	"github.com/belazy/monid-finance/providers"
)

// beneficialOwnerFiler is one distinct filer entry for
// get_beneficial_owners, named after the same reporting_person_name/
// filer_cik fields the Financial Datasets BeneficialOwner contract uses
// for a filer (the Financial Datasets contract (captured 2026-09-04)), even though this
// directory shape itself is not one of that contract's response types.
type beneficialOwnerFiler struct {
	ReportingPersonName *string `json:"reporting_person_name,omitempty"`
	FilerCIK            *string `json:"filer_cik,omitempty"`
}

// getBeneficialOwners answers get_beneficial_owners: the distinct filers
// (reporting persons) named across the 13D and 13G feeds, optionally
// narrowed by a case-insensitive name prefix and capped by limit.
func (c *callCtx) getBeneficialOwners(args map[string]any) (Result, error) {
	nameArg, err := argString(args, "name")
	if err != nil {
		return Result{}, err
	}
	limitRaw, err := argIntDefault(args, "limit", 100)
	if err != nil {
		return Result{}, err
	}
	limit, err := validateLimit(limitRaw, 1000)
	if err != nil {
		return Result{}, err
	}

	dRows, gRows, err := c.fetchBeneficialFeeds()
	if err != nil {
		return Result{}, err
	}

	seen := make(map[string]bool)
	var filers []beneficialOwnerFiler
	collect := func(rows []map[string]any) {
		for _, row := range rows {
			filerName := firstStringGeneric(row, "filed_by_symbol", "filed_by_name", "filer_name", "filed_by")
			if filerName == nil {
				continue
			}
			key := strings.ToLower(*filerName)
			if seen[key] {
				continue
			}
			seen[key] = true
			cik := firstStringGeneric(row, "filed_by_cik", "filer_cik", "cik")
			filers = append(filers, beneficialOwnerFiler{ReportingPersonName: filerName, FilerCIK: cik})
		}
	}
	collect(dRows)
	collect(gRows)

	if nameArg != nil && strings.TrimSpace(*nameArg) != "" {
		prefix := strings.ToLower(strings.TrimSpace(*nameArg))
		filtered := make([]beneficialOwnerFiler, 0, len(filers))
		for _, f := range filers {
			if strings.HasPrefix(strings.ToLower(*f.ReportingPersonName), prefix) {
				filtered = append(filtered, f)
			}
		}
		filers = filtered
	}
	sort.SliceStable(filers, func(i, j int) bool {
		return strings.ToLower(*filers[i].ReportingPersonName) < strings.ToLower(*filers[j].ReportingPersonName)
	})
	if len(filers) > limit {
		filers = filers[:limit]
	}

	out := make([]any, len(filers))
	for i, f := range filers {
		out[i] = f
	}
	return Result{Value: out, WrapperKey: "beneficial_owners", Paginate: true}, nil
}

// beneficialOwnershipRow is one stake row for get_beneficial_ownership,
// field-named and ordered after the Financial Datasets BeneficialOwner
// contract (the Financial Datasets contract (captured 2026-09-04)) as far as this port can
// source it from the SECForm4 13D/13G feed; every contract field this
// feed never carries (issuer_cik, voting/dispositive powers,
// purpose_of_transaction, ...) is left off the struct entirely rather
// than kept as an always-nil placeholder. ShareChange/ShareChangePercent
// are not part of the FD contract: they carry SECForm4's
// shares_vs_prev_report figure, which is genuinely sourced and worth
// keeping rather than discarding.
type beneficialOwnershipRow struct {
	Ticker                           *string  `json:"ticker,omitempty"`
	FilerCIK                         *string  `json:"filer_cik,omitempty"`
	ReportingPersonName              *string  `json:"reporting_person_name,omitempty"`
	FormType                         *string  `json:"form_type,omitempty"`
	Type                             *string  `json:"type,omitempty"`
	FilingDate                       *string  `json:"filing_date,omitempty"`
	EventDate                        *string  `json:"event_date,omitempty"`
	AggregateAmountBeneficiallyOwned *float64 `json:"aggregate_amount_beneficially_owned,omitempty"`
	PercentOfClass                   *float64 `json:"percent_of_class,omitempty"`
	ShareChange                      *float64 `json:"share_change,omitempty"`
	ShareChangePercent               *float64 `json:"share_change_percent,omitempty"`
}

// edgarFormFor maps the type filter onto the EDGAR form it selects.
func edgarFormFor(typeArg *string) string {
	if typeArg == nil {
		return ""
	}
	switch *typeArg {
	case "activist":
		return "SC 13D"
	case "passive":
		return "SC 13G"
	}
	return ""
}

// fetchBeneficialFeeds runs /get_13d_filings and /get_13g_filings
// concurrently and returns each feed's rows.
func (c *callCtx) fetchBeneficialFeeds() (dRows, gRows []map[string]any, err error) {
	var dRun *monid.Run
	var dErr error
	var gRun *monid.Run
	var gErr error
	concurrent2(
		func() { dRun, dErr = c.run(secform4, beneficial13DEndpoint, nil, nil) },
		func() { gRun, gErr = c.run(secform4, beneficial13GEndpoint, nil, nil) },
	)
	if dErr != nil {
		return nil, nil, dErr
	}
	if gErr != nil {
		return nil, nil, gErr
	}
	dRows, err = statusEnvelopeRows(dRun.Output, "13D filings")
	if err != nil {
		return nil, nil, err
	}
	gRows, err = statusEnvelopeRows(gRun.Output, "13G filings")
	if err != nil {
		return nil, nil, err
	}
	return dRows, gRows, nil
}

// getBeneficialOwnership answers get_beneficial_ownership: stakes from the
// 13D/13G feed for one ticker (who owns this company) or one filer_cik
// (what stakes this filer holds) - exactly one is required, mirroring
// get_institutional_holdings' own ticker/filer_cik exclusivity. type
// narrows to one feed; without it, both feeds are scanned. history=true
// is accepted for schema parity but rejected: this feed carries only each
// stake's latest reported state, never a full amendment chain, the same
// honest-rejection shape get_institutional_holdings uses for filer_cik.
func (c *callCtx) getBeneficialOwnership(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	filerCIKArg, err := argString(args, "filer_cik")
	if err != nil {
		return Result{}, err
	}
	if (tickerArg == nil) == (filerCIKArg == nil) {
		return Result{}, &providers.InputError{Msg: "exactly one of ticker or filer_cik is required"}
	}
	typeArg, err := argString(args, "type")
	if err != nil {
		return Result{}, err
	}
	if typeArg != nil && *typeArg != "activist" && *typeArg != "passive" {
		return Result{}, &providers.InputError{Msg: "type must be activist or passive"}
	}
	history, err := argBoolDefault(args, "history", false)
	if err != nil {
		return Result{}, err
	}
	if history {
		return Result{Value: fd.NewErrorResponse("bad_request",
			"history=true is not supported; this server returns only each stake's current reported state")}, nil
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

	var symbol string
	if tickerArg != nil {
		symbol, err = validateTicker(*tickerArg)
		if err != nil {
			return Result{}, err
		}
	}
	var filerCIK string
	if filerCIKArg != nil {
		filerCIK = normalizeCIKArg(*filerCIKArg)
		if filerCIK == "" {
			return Result{}, &providers.InputError{Msg: "filer_cik must contain digits"}
		}
	}

	var dRows, gRows []map[string]any
	switch {
	case typeArg != nil && *typeArg == "activist":
		run, rerr := c.run(secform4, beneficial13DEndpoint, nil, nil)
		if rerr != nil {
			return Result{}, rerr
		}
		dRows, err = statusEnvelopeRows(run.Output, "13D filings")
		if err != nil {
			return Result{}, err
		}
	case typeArg != nil && *typeArg == "passive":
		run, rerr := c.run(secform4, beneficial13GEndpoint, nil, nil)
		if rerr != nil {
			return Result{}, rerr
		}
		gRows, err = statusEnvelopeRows(run.Output, "13G filings")
		if err != nil {
			return Result{}, err
		}
	default:
		dRows, gRows, err = c.fetchBeneficialFeeds()
		if err != nil {
			return Result{}, err
		}
	}

	// Two paths to the same fact. The SECForm4 feed is one short rolling
	// window across all issuers, which most tickers are absent from, so a
	// ticker query falls through to that issuer's own Schedule 13 index at
	// EDGAR rather than answering an empty list.
	rows, err := firstNonEmpty(
		func() ([]beneficialOwnershipRow, error) {
			feed := buildBeneficialOwnershipRows(dRows, "SCHEDULE 13D", "activist", symbol, filerCIK, filingDate)
			return append(feed, buildBeneficialOwnershipRows(gRows, "SCHEDULE 13G", "passive", symbol, filerCIK, filingDate)...), nil
		},
		func() ([]beneficialOwnershipRow, error) {
			if symbol == "" {
				return nil, nil
			}
			return c.beneficialFromEDGAR(symbol, edgarFormFor(typeArg), limit)
		},
	)
	if err != nil {
		return Result{}, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return beneficialRowDate(rows[i]) > beneficialRowDate(rows[j])
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}

	out := make([]any, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "beneficial_owners", Paginate: true}, nil
}

func beneficialRowDate(r beneficialOwnershipRow) string {
	if r.FilingDate != nil {
		return *r.FilingDate
	}
	return ""
}

// buildBeneficialOwnershipRows filters rows to symbol (company_symbol) or
// filerCIK (matched against whichever CIK-like alias a row happens to
// carry - the verified 13D/13G item shape has none, so a filer_cik query
// legitimately returns no rows rather than a guess), applies filingDate,
// and shapes what remains into beneficialOwnershipRow, parsing the
// compound shares_owned_owned/shares_vs_prev_report fields.
func buildBeneficialOwnershipRows(rows []map[string]any, formType, stakeType, symbol, filerCIK string, filingDate dateFilters) []beneficialOwnershipRow {
	out := make([]beneficialOwnershipRow, 0, len(rows))
	for _, row := range rows {
		companySymbol := firstStringGeneric(row, "company_symbol")
		if symbol != "" {
			if companySymbol == nil || !strings.EqualFold(*companySymbol, symbol) {
				continue
			}
		}
		if filerCIK != "" {
			rowCIK := firstStringGeneric(row, "filed_by_cik", "filer_cik", "cik")
			if rowCIK == nil || normalizeCIKArg(*rowCIK) != filerCIK {
				continue
			}
		}
		reportedRaw := firstStringGeneric(row, "reported_datetime")
		var filingDay *string
		if reportedRaw != nil {
			if day, ok := secform4DateOnly(*reportedRaw); ok {
				filingDay = &day
			}
		}
		if filingDate.any() {
			if filingDay == nil {
				continue
			}
			day, perr := validateDate(filingDay, "filing_date")
			if perr != nil || day == nil || !filingDate.matches(*day) {
				continue
			}
		}
		transactionRaw := firstStringGeneric(row, "transaction_date")
		var eventDay *string
		if transactionRaw != nil {
			if day, ok := secform4DateOnly(*transactionRaw); ok {
				eventDay = &day
			}
		}
		filerName := firstStringGeneric(row, "filed_by_symbol", "filed_by_name", "filer_name", "filed_by")
		filerCIKValue := firstStringGeneric(row, "filed_by_cik", "filer_cik", "cik")

		record := beneficialOwnershipRow{
			ReportingPersonName: filerName,
			FilerCIK:            filerCIKValue,
			FilingDate:          filingDay,
			EventDate:           eventDay,
		}
		if symbol != "" {
			s := symbol
			record.Ticker = &s
		} else if companySymbol != nil {
			record.Ticker = companySymbol
		}
		t := formType
		record.FormType = &t
		st := stakeType
		record.Type = &st
		if ownedRaw := firstStringGeneric(row, "shares_owned_owned"); ownedRaw != nil {
			record.AggregateAmountBeneficiallyOwned, record.PercentOfClass = parseSharesAndPercent(*ownedRaw)
		}
		if changeRaw := firstStringGeneric(row, "shares_vs_prev_report"); changeRaw != nil {
			record.ShareChange, record.ShareChangePercent = parseDeltaAndPercent(*changeRaw)
		}
		out = append(out, record)
	}
	return out
}

// normalizeCIKArg strips everything but ASCII digits and leading zeros
// from a caller-supplied or row-sourced CIK-like value, so "0000320193",
// "320193", and " 320193 " all compare equal. Returns "" when nothing
// digit-shaped remains.
func normalizeCIKArg(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "0")
}
