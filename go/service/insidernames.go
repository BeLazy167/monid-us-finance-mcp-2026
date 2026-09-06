// The insider directory behind /insider-trades/names.
//
// An agent asks about a person by the name a human uses ("Jensen Huang"),
// while the SEC filing spells it "HUANG JEN HSUN". Financial Datasets
// publishes the filed spellings for one issuer so a client can resolve
// one to the other before filtering trades by name. Without the route a
// client cannot discover what spellings exist, and its name filter
// matches nothing while reporting no error: measured 2026-09-05, the
// Dexter research agent called this route first for exactly that reason
// and its whole insider-trade path failed on the 404.
//
// The names come from the same SECForm4 feed /insider-trades reads, so
// the directory can never name an insider whose trades this server
// cannot then return.
package service

import (
	"sort"
	"strings"

	"github.com/belazy/monid-finance/providers"
)

// insiderNamesLimit is how many trades the directory reads. A directory
// that listed only the most recent page's insiders would omit anyone who
// last traded earlier, so this takes the whole feed rather than a page of
// it. It is the same ceiling /insider-trades accepts.
const insiderNamesLimit = 5000

// getInsiderNames answers with every distinct insider on file for one
// issuer, in alphabetical order.
func (c *callCtx) getInsiderNames(args map[string]any) (Result, error) {
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
	run, err := c.run(secform4, insiderEndpoint, nil, map[string]any{"query": symbol})
	if err != nil {
		return Result{}, err
	}
	trades, err := providers.NormalizeInsiderTrades(
		run.Output, symbol, insiderNamesLimit, nil, nil, nil, nil, nil)
	if err != nil {
		return Result{}, err
	}

	seen := make(map[string]bool, len(trades))
	names := make([]string, 0, len(trades))
	for _, trade := range trades {
		if trade.Name == nil {
			continue
		}
		name := strings.TrimSpace(*trade.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	// Returned under a wrapper key so the REST layer can name the resource
	// and echo the issuer alongside the list, which is the envelope
	// Financial Datasets answers with.
	return Result{Value: names, WrapperKey: "names"}, nil
}
