// The market-derived half of financial metrics.
//
// A statement says what a company earned and owns; it never says what the
// market charges for it. Without a price, market_cap and every multiple
// built on one are absent, and this server used to omit all of them.
// That is not a cosmetic gap: every LLM investor agent in a fund is asked
// to judge whether the price is sensible, so a record with no P/E makes
// each of them abstain. Measured 2026-09-05, GLM 5.3 read an Apple
// snapshot from this server and answered "the filings show no market cap
// and no P/E, and I never buy a wonderful business without first being
// told the price".
//
// The price comes from this server's own OHLCV feed, taken at each row's
// own report period, so a historical row is valued at the price that
// stood then rather than today's.
package service

import (
	"math"
	"time"

	"github.com/belazy/monid-finance/providers"
)

// valuationInputs are the statement lines a multiple is built from.
type valuationInputs struct {
	Shares          float64
	EPS             float64
	Revenue         float64
	Equity          float64
	Debt            float64
	Cash            float64
	EBITDA          float64
	FreeCashFlow    float64
	EPSGrowth       float64
	hasShares       bool
	hasEPS          bool
	hasRevenue      bool
	hasEquity       bool
	hasFreeCashFlow bool
	hasEBITDA       bool
}

// closesByDate maps a trading day to that day's close.
type closesByDate map[string]float64

// dailyCloses reads the OHLCV feed into a date-keyed close series.
func (c *callCtx) dailyCloses(ticker string) (closesByDate, []time.Time, error) {
	run, err := c.run(defillama, ohlcvEndpoint, nil,
		map[string]any{"ticker": ticker, "country": "US", "timeframe": "MAX"})
	if err != nil {
		return nil, nil, err
	}
	bars, err := providers.NormalizePrices(run.Output, "", "", "day")
	if err != nil {
		return nil, nil, err
	}
	closes := closesByDate{}
	days := make([]time.Time, 0, len(bars))
	for _, bar := range bars {
		if bar.Time == nil || bar.Close == nil {
			continue
		}
		day := (*bar.Time)[:10]
		if parsed, perr := time.Parse(dateLayout, day); perr == nil {
			closes[day] = *bar.Close
			days = append(days, parsed)
		}
	}
	return closes, days, nil
}

// closeOnOrBefore is the last close up to asOf. A report period usually
// lands on a weekend or a holiday, so an exact-date lookup finds nothing.
func closeOnOrBefore(closes closesByDate, days []time.Time, asOf time.Time) (float64, bool) {
	var best time.Time
	found := false
	for _, day := range days {
		if day.After(asOf) {
			continue
		}
		if !found || day.After(best) {
			best, found = day, true
		}
	}
	if !found {
		return 0, false
	}
	return closes[best.Format(dateLayout)], true
}

// number reads one statement line, reporting whether it was stated.
func number(row map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := row[key].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		}
	}
	return 0, false
}

// inputsFor gathers the lines one period needs from the three statements.
// The statement matrices are keyed by the provider's own printed labels
// ("Shares Outstanding (Diluted)"), not by Financial Datasets field
// names. The normalised names are kept as fallbacks so this keeps working
// if the shape is ever normalised upstream.
func inputsFor(income, balance, cash map[string]any, epsGrowth float64) valuationInputs {
	in := valuationInputs{EPSGrowth: epsGrowth}
	in.Shares, in.hasShares = number(income,
		"Shares Outstanding (Diluted)", "Shares Outstanding (Basic)",
		"weighted_average_shares_diluted", "weighted_average_shares")
	in.EPS, in.hasEPS = number(income,
		"EPS (Diluted)", "EPS (Basic)", "earnings_per_share_diluted", "earnings_per_share")
	in.Revenue, in.hasRevenue = number(income, "Revenue", "revenue")
	in.Equity, in.hasEquity = number(balance,
		"Total Shareholders Equity", "Total Equity", "shareholders_equity")
	in.EBITDA, in.hasEBITDA = number(income, "EBITDA", "ebitda")
	in.FreeCashFlow, in.hasFreeCashFlow = number(cash, "Free Cash Flow", "free_cash_flow")

	shortDebt, _ := number(balance, "Total Current Liabilities|Short-Term Debt", "current_debt")
	longDebt, _ := number(balance, "Total Non-Current Liabilities|Long-Term Debt", "non_current_debt")
	if total, ok := number(balance, "total_debt"); ok {
		in.Debt = total
	} else {
		in.Debt = shortDebt + longDebt
	}
	in.Cash, _ = number(balance,
		"Total Current Assets|Cash and Cash Equivalents", "cash_and_equivalents")
	return in
}

// valuationFields derives every market-relative metric a price makes
// available. A field whose inputs are missing or would divide by zero is
// left out rather than reported as zero.
func valuationFields(price float64, in valuationInputs) map[string]any {
	out := map[string]any{}
	set := func(key string, value float64) {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return
		}
		out[key] = value
	}

	var marketCap float64
	if in.hasShares && in.Shares > 0 {
		marketCap = price * in.Shares
		set("market_cap", marketCap)
		if in.hasEquity {
			bvps := in.Equity / in.Shares
			set("book_value_per_share", bvps)
			if bvps > 0 {
				set("price_to_book_ratio", price/bvps)
			}
		}
		if in.hasFreeCashFlow {
			set("free_cash_flow_per_share", in.FreeCashFlow/in.Shares)
		}
	}
	if in.hasEPS && in.EPS > 0 {
		pe := price / in.EPS
		set("price_to_earnings_ratio", pe)
		// PEG compares the multiple with the growth rate behind it, and
		// only means anything while earnings are growing.
		if in.EPSGrowth > 0 {
			set("peg_ratio", pe/(in.EPSGrowth*100))
		}
	}
	if marketCap > 0 {
		if in.hasRevenue && in.Revenue > 0 {
			set("price_to_sales_ratio", marketCap/in.Revenue)
		}
		if in.hasFreeCashFlow {
			set("free_cash_flow_yield", in.FreeCashFlow/marketCap)
		}
		enterprise := marketCap + in.Debt - in.Cash
		set("enterprise_value", enterprise)
		if in.hasRevenue && in.Revenue > 0 {
			set("enterprise_value_to_revenue_ratio", enterprise/in.Revenue)
		}
		if in.hasEBITDA && in.EBITDA > 0 {
			set("enterprise_value_to_ebitda_ratio", enterprise/in.EBITDA)
		}
	}
	return out
}

// withValuation adds the market-relative metrics to each row, priced at
// that row's own report period. One OHLCV call serves every row, and a
// row whose period has no close is returned untouched.
func (c *callCtx) withValuation(ticker, period string, statements any, rows []map[string]any) []map[string]any {
	if len(rows) == 0 {
		return rows
	}
	closes, days, err := c.dailyCloses(ticker)
	if err != nil || len(days) == 0 {
		return rows
	}
	income, ierr := providers.ParseStatementSeries(statements, "income")
	balance, berr := providers.ParseStatementSeries(statements, "balance")
	cash, cerr := providers.ParseStatementSeries(statements, "cash")
	if ierr != nil || berr != nil || cerr != nil {
		return rows
	}
	annual := period == "annual"
	for _, row := range rows {
		reportPeriod, ok := row["report_period"].(string)
		if !ok {
			continue
		}
		asOf, perr := time.Parse(dateLayout, reportPeriod)
		if perr != nil {
			continue
		}
		price, found := closeOnOrBefore(closes, days, asOf)
		if !found || price <= 0 {
			continue
		}
		growth, _ := row["earnings_per_share_growth"].(float64)
		in := inputsFor(
			periodValues(income, asOf, annual),
			periodValues(balance, asOf, annual),
			periodValues(cash, asOf, annual),
			growth,
		)
		for key, value := range valuationFields(price, in) {
			row[key] = value
		}
	}
	return rows
}

// periodValues finds the statement row for one report period. A ttm row
// has no statement of its own, so it reads the newest one, which is the
// window it was built from.
func periodValues(series providers.StatementSeries, asOf time.Time, annual bool) map[string]any {
	rows := series.Quarterly
	if annual {
		rows = series.Annual
	}
	var best providers.PeriodRow
	found := false
	for _, row := range rows {
		if row.ReportPeriod.After(asOf) {
			continue
		}
		if !found || row.ReportPeriod.After(best.ReportPeriod) {
			best, found = row, true
		}
	}
	if !found {
		return map[string]any{}
	}
	return best.Values
}
