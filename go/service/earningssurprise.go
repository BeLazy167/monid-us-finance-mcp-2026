// Analyst consensus, and the earnings surprise derived from it.
//
// The statements feed states what a company reported, never what the
// street expected, so an earnings record built from it alone carries no
// surprise. That is not cosmetic: a post-earnings-drift strategy reads
// eps_surprise to decide whether to be long or short, and a record
// without one makes the whole model neutral. Nasdaq publishes the
// consensus and the reported figure side by side, which is enough to
// state the estimate, the verdict and the gap.
package service

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/belazy/monid-finance/fd"
)

const nasdaqEarningsEndpoint = "/get_stock_earnings"

// epsConsensus is one quarter's expectation and outcome.
type epsConsensus struct {
	Consensus float64
	Reported  float64
}

// consensusByQuarter fetches the consensus history and keys it by the
// month label Nasdaq reports it under ("Sep 2025"), which is how a
// report period is matched back to it.
func (c *callCtx) consensusByQuarter(ticker string) (map[string]epsConsensus, error) {
	run, err := c.run(nasdaq, nasdaqEarningsEndpoint, nil, map[string]any{"symbol": ticker})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data struct {
			EPS struct {
				EarningsPerShare []struct {
					Period    string   `json:"period"`
					Consensus *float64 `json:"consensus"`
					Earnings  *float64 `json:"earnings"`
				} `json:"earningsPerShare"`
			} `json:"eps"`
		} `json:"data"`
	}
	if jerr := json.Unmarshal(run.Output, &payload); jerr != nil {
		return nil, nil
	}
	out := map[string]epsConsensus{}
	for _, row := range payload.Data.EPS.EarningsPerShare {
		// A quarter that has not reported carries a zero outcome; it has
		// no surprise to state.
		if row.Consensus == nil || row.Earnings == nil || *row.Earnings == 0 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(row.Period))] = epsConsensus{
			Consensus: *row.Consensus, Reported: *row.Earnings,
		}
	}
	return out, nil
}

// quarterKey renders a report period the way Nasdaq labels it, so
// 2025-09-27 matches "Sep 2025".
func quarterKey(reportPeriod string) string {
	day, err := time.Parse(dateLayout, reportPeriod)
	if err != nil {
		return ""
	}
	return strings.ToLower(day.Format("Jan 2006"))
}

// surpriseVerdict is the word Financial Datasets reports for the gap
// between what was expected and what was reported.
func surpriseVerdict(reported, consensus float64) string {
	switch {
	case reported > consensus:
		return "BEAT"
	case reported < consensus:
		return "MISS"
	default:
		return "MEET"
	}
}

// withEarningsSurprise adds the estimate and the surprise to each
// record's quarterly block. A record whose quarter has no consensus is
// left exactly as it was rather than carrying an invented one.
func (c *callCtx) withEarningsSurprise(ticker string, records []fd.EarningsRecord) []fd.EarningsRecord {
	consensus, err := c.consensusByQuarter(ticker)
	if err != nil || len(consensus) == 0 {
		return records
	}
	for i := range records {
		if records[i].ReportPeriod == nil || len(records[i].Quarterly) == 0 {
			continue
		}
		match, ok := consensus[quarterKey(*records[i].ReportPeriod)]
		if !ok {
			continue
		}
		var block map[string]any
		if jerr := json.Unmarshal(records[i].Quarterly, &block); jerr != nil {
			continue
		}
		block["estimated_earnings_per_share"] = match.Consensus
		block["eps_surprise"] = surpriseVerdict(match.Reported, match.Consensus)
		if match.Consensus != 0 {
			pct := (match.Reported - match.Consensus) / math.Abs(match.Consensus) * 100
			block["eps_surprise_pct"] = roundTo(pct, 2)
		}
		if _, present := block["earnings_per_share"]; !present {
			block["earnings_per_share"] = match.Reported
		}
		if encoded, merr := json.Marshal(block); merr == nil {
			records[i].Quarterly = encoded
		}
	}
	return records
}

// roundTo keeps a derived percentage readable rather than carrying the
// full float expansion of a division.
func roundTo(v float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	rounded := math.Round(v*scale) / scale
	if math.IsNaN(rounded) || math.IsInf(rounded, 0) {
		return 0
	}
	return rounded
}
