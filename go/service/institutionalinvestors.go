// This file builds get_institutional_investors: a directory of distinct
// SEC 13F filers (institutional investors), discovered by scanning
// SECForm4's /get_institution_holders route unscoped (no ticker), the
// same way get_beneficial_owners scans the 13D/13G feed for distinct
// filers. Field names follow the Financial Datasets InstitutionalInvestor
// contract for this route (cik/name - docs/fd-contract-reference.json),
// NOT fd.InstitutionalHolding's filer_cik/filer_name pair, even though
// get_institutional_holdings sources the same two values from this same
// route: Financial Datasets names them differently on the two endpoints,
// and parity follows the endpoint being served. The provider aliases read
// below still include the filer_* spellings, because those are what the
// upstream feed itself uses.
package service

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

// institutionalInvestor is one distinct filer entry for
// get_institutional_investors.
type institutionalInvestor struct {
	CIK  *string `json:"cik,omitempty"`
	Name *string `json:"name,omitempty"`
}

// getInstitutionalInvestors answers get_institutional_investors: the
// unscoped SECForm4 institution-holders feed carries rows across many
// issuers, so this dedupes them down to one row per distinct filer name,
// optionally narrowed by a case-insensitive name prefix. Unlike
// get_institutional_holdings, this tool's schema takes no ticker or
// filer_cik (only an optional name filter), so there is no per-filer
// lookup here for /get_hedge_fund_portfolio to serve; that route stays
// reserved for a tool whose schema actually accepts a filer.
func (c *callCtx) getInstitutionalInvestors(args map[string]any) (Result, error) {
	nameArg, err := argString(args, "name")
	if err != nil {
		return Result{}, err
	}

	run, err := c.run(secform4, institutionalEndpoint, nil, nil)
	if err != nil {
		return Result{}, err
	}

	var value any
	if err := json.Unmarshal(run.Output, &value); err != nil {
		return Result{}, &providers.SchemaDriftError{Msg: "SECForm4 institution holders payload must be valid JSON"}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return Result{}, &providers.SchemaDriftError{Msg: "SECForm4 institution holders payload must be an object"}
	}
	if status, _ := root["status"].(string); status != "success" {
		return Result{}, &providers.SchemaDriftError{Msg: "SECForm4 institution holders status must be 'success'"}
	}
	var rows []map[string]any
	switch data := root["data"].(type) {
	case []any:
		rows, err = instHoldingsObjectList(data, "SECForm4 institution holders rows")
	case map[string]any:
		rows, err = instHoldingsRows(data)
	default:
		return Result{}, &providers.SchemaDriftError{Msg: "SECForm4 institution holders omitted a data object"}
	}
	if err != nil {
		return Result{}, err
	}

	seen := make(map[string]bool, len(rows))
	investors := make([]institutionalInvestor, 0, len(rows))
	for _, row := range rows {
		filerName := firstStringGeneric(row, "filer_name", "institution", "holder", "manager", "name")
		if filerName == nil {
			continue
		}
		key := strings.ToLower(*filerName)
		if seen[key] {
			continue
		}
		seen[key] = true
		cik := firstStringGeneric(row, "filer_cik", "institution_cik", "manager_cik", "cik")
		investors = append(investors, institutionalInvestor{CIK: cik, Name: filerName})
	}

	if nameArg != nil && strings.TrimSpace(*nameArg) != "" {
		prefix := strings.ToLower(strings.TrimSpace(*nameArg))
		filtered := make([]institutionalInvestor, 0, len(investors))
		for _, inv := range investors {
			if strings.HasPrefix(strings.ToLower(*inv.Name), prefix) {
				filtered = append(filtered, inv)
			}
		}
		investors = filtered
	}
	sort.SliceStable(investors, func(i, j int) bool {
		return strings.ToLower(*investors[i].Name) < strings.ToLower(*investors[j].Name)
	})

	out := make([]any, len(investors))
	for i, inv := range investors {
		out[i] = inv
	}
	return Result{Value: out, WrapperKey: "investors", Paginate: true}, nil
}
