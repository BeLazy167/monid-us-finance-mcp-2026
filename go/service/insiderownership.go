// This file builds get_insider_ownership from SECForm4's
// /get_company_insider_trading route: a CIK-keyed feed of one company's
// full insider trading history. This tool answers "what does each insider
// currently hold" (a state), which this port derives by aggregating that
// history down to each insider's most recent post-transaction shares_owned
// figure - the same number a Form 3/5 ownership statement would report,
// just reached through the trade history this route actually offers
// rather than a Forms 3/5 feed this server does not have.
//
// The payload shape below is not independently verified the way
// /get_13d_filings and /get_13g_filings were (see ownershipshared.go and
// docs/compatibility.md): it is assumed to share /search's row shape
// (reported_datetime, transaction_date, insider_relationship, company,
// shares_owned, ...), the same provider family's only verified insider
// route (providers/insider.go). Every field read below therefore goes
// through firstStringGeneric's alias list rather than a single required
// key, so a shape drift degrades to an omitted field instead of a hard
// failure wherever that is safe.
package service

import (
	"sort"
	"strings"

	"github.com/belazy/monid-finance/fd"
	"github.com/belazy/monid-finance/providers"
)

// insiderOwnershipRow is one insider's current holding for
// get_insider_ownership, field-named and ordered after the Financial
// Datasets InsiderOwnership contract (docs/fd-contract-reference.json).
// Fields this port cannot source from SECForm4's insider-trading feed
// (title, is_board_director/is_officer/is_ten_percent_owner, the
// derivative-security fields) are left off the struct entirely rather
// than kept as an always-nil placeholder.
type insiderOwnershipRow struct {
	Ticker           *string  `json:"ticker,omitempty"`
	Issuer           *string  `json:"issuer,omitempty"`
	Name             *string  `json:"name,omitempty"`
	FormType         *string  `json:"form_type,omitempty"`
	FilingDate       *string  `json:"filing_date,omitempty"`
	AsOfDate         *string  `json:"as_of_date,omitempty"`
	AccessionNumber  *string  `json:"accession_number,omitempty"`
	SharesOwned      *float64 `json:"shares_owned,omitempty"`
	DirectOrIndirect *string  `json:"direct_or_indirect,omitempty"`
}

// insiderOwnershipObservation is one raw insider-trading history row,
// before aggregation down to one row per insider.
type insiderOwnershipObservation struct {
	name             string
	company          *string
	filingDate       *string // reported_datetime's date component, YYYY-MM-DD
	asOfDate         *string // transaction_date's date component, YYYY-MM-DD
	formType         *string
	accessionNumber  *string
	sharesOwned      *float64
	directOrIndirect *string
}

// getInsiderOwnership answers get_insider_ownership: ticker is required
// (this tool has no company-independent directory mode), so its CIK is
// derived via resolveIssuerCIK before any paid insider-trading call, per
// this port's brief.
func (c *callCtx) getInsiderOwnership(args map[string]any) (Result, error) {
	tickerArg, err := argString(args, "ticker")
	if err != nil {
		return Result{}, err
	}
	if tickerArg == nil {
		return Result{}, &providers.InputError{Msg: "ticker is required"}
	}
	symbol, err := validateTicker(*tickerArg)
	if err != nil {
		return Result{}, err
	}
	nameArg, err := argString(args, "name")
	if err != nil {
		return Result{}, err
	}
	formTypeArg, err := argString(args, "form_type")
	if err != nil {
		return Result{}, err
	}
	if formTypeArg != nil {
		return Result{Value: fd.NewErrorResponse("bad_request",
			"form_type is not supported; the underlying insider-trading feed carries no Form 3/5 classification")}, nil
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

	cik, found, err := c.resolveIssuerCIK(symbol)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{Value: fd.NewErrorResponse("not_found",
			"No SEC CIK could be resolved for ticker "+symbol+".")}, nil
	}

	run, err := c.run(secform4, insiderTradingEndpoint, nil, map[string]any{"cik": cik})
	if err != nil {
		return Result{}, err
	}
	rows, err := statusEnvelopeRows(run.Output, "company insider trading")
	if err != nil {
		return Result{}, err
	}

	observations := make([]insiderOwnershipObservation, 0, len(rows))
	for _, row := range rows {
		name := firstStringGeneric(row, "insider_relationship", "insider_name", "name", "reporting_owner", "reportingOwner")
		if name == nil {
			continue
		}
		obs := insiderOwnershipObservation{name: *name}
		if company := firstStringGeneric(row, "company", "company_name", "issuer"); company != nil {
			obs.company = company
		}
		if reported := firstStringGeneric(row, "reported_datetime", "reportedDatetime"); reported != nil {
			if day, ok := secform4DateOnly(*reported); ok {
				obs.filingDate = &day
			}
		}
		if transaction := firstStringGeneric(row, "transaction_date", "transactionDate"); transaction != nil {
			if day, ok := secform4DateOnly(*transaction); ok {
				obs.asOfDate = &day
			}
		}
		if formType := firstStringGeneric(row, "form_type", "formType", "form"); formType != nil {
			obs.formType = formType
		}
		if viewURL := firstStringGeneric(row, "view_url", "filing_url", "url"); viewURL != nil {
			if accession := deriveAccessionLocal(*viewURL); accession != nil {
				obs.accessionNumber = accession
			}
		}
		if sharesRaw := firstStringGeneric(row, "shares_owned", "sharesOwned", "current_shares", "shares"); sharesRaw != nil {
			obs.sharesOwned, obs.directOrIndirect = parseSharesAndOwnership(*sharesRaw)
		}
		observations = append(observations, obs)
	}

	latest := latestPerInsider(observations)

	filtered := make([]insiderOwnershipRow, 0, len(latest))
	for _, obs := range latest {
		if nameArg != nil && !strings.Contains(strings.ToLower(obs.name), strings.ToLower(*nameArg)) {
			continue
		}
		if filingDate.any() {
			if obs.filingDate == nil {
				continue
			}
			day, perr := validateDate(obs.filingDate, "filing_date")
			if perr != nil || day == nil || !filingDate.matches(*day) {
				continue
			}
		}
		name := obs.name
		record := insiderOwnershipRow{
			Ticker:           &symbol,
			Issuer:           obs.company,
			Name:             &name,
			FormType:         obs.formType,
			FilingDate:       obs.filingDate,
			AsOfDate:         obs.asOfDate,
			AccessionNumber:  obs.accessionNumber,
			SharesOwned:      obs.sharesOwned,
			DirectOrIndirect: obs.directOrIndirect,
		}
		filtered = append(filtered, record)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		di, dj := "", ""
		if filtered[i].FilingDate != nil {
			di = *filtered[i].FilingDate
		}
		if filtered[j].FilingDate != nil {
			dj = *filtered[j].FilingDate
		}
		if di != dj {
			return di > dj
		}
		return strings.ToLower(*filtered[i].Name) < strings.ToLower(*filtered[j].Name)
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	out := make([]any, len(filtered))
	for i, r := range filtered {
		out[i] = r
	}
	return Result{Value: out, WrapperKey: "insider_ownership", Paginate: true}, nil
}

// latestPerInsider reduces observations to one per distinct insider name
// (case-insensitive), keeping the observation with the latest filingDate
// (a nil/unparseable filingDate sorts before any dated one); ties keep the
// last-encountered observation for that day, mirroring feed order.
func latestPerInsider(observations []insiderOwnershipObservation) []insiderOwnershipObservation {
	best := make(map[string]insiderOwnershipObservation)
	order := make([]string, 0, len(observations))
	for _, obs := range observations {
		key := strings.ToLower(obs.name)
		current, exists := best[key]
		if !exists {
			order = append(order, key)
			best[key] = obs
			continue
		}
		if obsDateGTE(obs, current) {
			best[key] = obs
		}
	}
	out := make([]insiderOwnershipObservation, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	return out
}

func obsDateGTE(a, b insiderOwnershipObservation) bool {
	ad, bd := "", ""
	if a.filingDate != nil {
		ad = *a.filingDate
	}
	if b.filingDate != nil {
		bd = *b.filingDate
	}
	return ad >= bd
}
