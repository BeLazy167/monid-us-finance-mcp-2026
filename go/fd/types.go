// Package fd holds the Financial Datasets record types.
//
// Field order defines JSON key order, matching the Financial Datasets
// OpenAPI property order exactly. Pointer fields with omitempty encode the
// contract rule that unsourced values are omitted, never fabricated.
// Generated from the Financial Datasets contract (captured 2026-09-04); do not hand-edit ordering.
package fd

import "encoding/json"

// CompanyFacts mirrors the Financial Datasets CompanyFacts schema.
type CompanyFacts struct {
	Ticker        *string `json:"ticker,omitempty"`
	Name          *string `json:"name,omitempty"`
	CIK           *string `json:"cik,omitempty"`
	Industry      *string `json:"industry,omitempty"`
	Sector        *string `json:"sector,omitempty"`
	Exchange      *string `json:"exchange,omitempty"`
	IsActive      *bool   `json:"is_active,omitempty"`
	Location      *string `json:"location,omitempty"`
	SECFilingsURL *string `json:"sec_filings_url,omitempty"`
	SICCode       *string `json:"sic_code,omitempty"`
	SICIndustry   *string `json:"sic_industry,omitempty"`
	SICSector     *string `json:"sic_sector,omitempty"`
}

// IncomeStatement mirrors the Financial Datasets IncomeStatement schema.
type IncomeStatement struct {
	Ticker                                  *string  `json:"ticker,omitempty"`
	ReportPeriod                            *string  `json:"report_period,omitempty"`
	FiscalPeriod                            *string  `json:"fiscal_period,omitempty"`
	Period                                  *string  `json:"period,omitempty"`
	Currency                                *string  `json:"currency,omitempty"`
	AccessionNumber                         *string  `json:"accession_number,omitempty"`
	FormType                                *string  `json:"form_type,omitempty"`
	FilingURL                               *string  `json:"filing_url,omitempty"`
	FilingDate                              *string  `json:"filing_date,omitempty"`
	FilingDatetime                          *string  `json:"filing_datetime,omitempty"`
	Revenue                                 *float64 `json:"revenue,omitempty"`
	CostOfRevenue                           *float64 `json:"cost_of_revenue,omitempty"`
	GrossProfit                             *float64 `json:"gross_profit,omitempty"`
	OperatingExpense                        *float64 `json:"operating_expense,omitempty"`
	SellingGeneralAndAdministrativeExpenses *float64 `json:"selling_general_and_administrative_expenses,omitempty"`
	ResearchAndDevelopment                  *float64 `json:"research_and_development,omitempty"`
	OperatingIncome                         *float64 `json:"operating_income,omitempty"`
	InterestExpense                         *float64 `json:"interest_expense,omitempty"`
	EBIT                                    *float64 `json:"ebit,omitempty"`
	IncomeTaxExpense                        *float64 `json:"income_tax_expense,omitempty"`
	NetIncomeDiscontinuedOperations         *float64 `json:"net_income_discontinued_operations,omitempty"`
	NetIncomeNonControllingInterests        *float64 `json:"net_income_non_controlling_interests,omitempty"`
	NetIncome                               *float64 `json:"net_income,omitempty"`
	NetIncomeCommonStock                    *float64 `json:"net_income_common_stock,omitempty"`
	PreferredDividendsImpact                *float64 `json:"preferred_dividends_impact,omitempty"`
	ConsolidatedIncome                      *float64 `json:"consolidated_income,omitempty"`
	EarningsPerShare                        *float64 `json:"earnings_per_share,omitempty"`
	EarningsPerShareDiluted                 *float64 `json:"earnings_per_share_diluted,omitempty"`
	DividendsPerCommonShare                 *float64 `json:"dividends_per_common_share,omitempty"`
	WeightedAverageShares                   *float64 `json:"weighted_average_shares,omitempty"`
	WeightedAverageSharesDiluted            *float64 `json:"weighted_average_shares_diluted,omitempty"`
}

// BalanceSheet mirrors the Financial Datasets BalanceSheet schema.
type BalanceSheet struct {
	Ticker                              *string  `json:"ticker,omitempty"`
	ReportPeriod                        *string  `json:"report_period,omitempty"`
	FiscalPeriod                        *string  `json:"fiscal_period,omitempty"`
	Period                              *string  `json:"period,omitempty"`
	Currency                            *string  `json:"currency,omitempty"`
	AccessionNumber                     *string  `json:"accession_number,omitempty"`
	FormType                            *string  `json:"form_type,omitempty"`
	FilingURL                           *string  `json:"filing_url,omitempty"`
	FilingDate                          *string  `json:"filing_date,omitempty"`
	FilingDatetime                      *string  `json:"filing_datetime,omitempty"`
	TotalAssets                         *float64 `json:"total_assets,omitempty"`
	CurrentAssets                       *float64 `json:"current_assets,omitempty"`
	CashAndEquivalents                  *float64 `json:"cash_and_equivalents,omitempty"`
	Inventory                           *float64 `json:"inventory,omitempty"`
	CurrentInvestments                  *float64 `json:"current_investments,omitempty"`
	TradeAndNonTradeReceivables         *float64 `json:"trade_and_non_trade_receivables,omitempty"`
	NonCurrentAssets                    *float64 `json:"non_current_assets,omitempty"`
	PropertyPlantAndEquipment           *float64 `json:"property_plant_and_equipment,omitempty"`
	GoodwillAndIntangibleAssets         *float64 `json:"goodwill_and_intangible_assets,omitempty"`
	Investments                         *float64 `json:"investments,omitempty"`
	NonCurrentInvestments               *float64 `json:"non_current_investments,omitempty"`
	OutstandingShares                   *float64 `json:"outstanding_shares,omitempty"`
	TaxAssets                           *float64 `json:"tax_assets,omitempty"`
	TotalLiabilities                    *float64 `json:"total_liabilities,omitempty"`
	CurrentLiabilities                  *float64 `json:"current_liabilities,omitempty"`
	CurrentDebt                         *float64 `json:"current_debt,omitempty"`
	TradeAndNonTradePayables            *float64 `json:"trade_and_non_trade_payables,omitempty"`
	DeferredRevenue                     *float64 `json:"deferred_revenue,omitempty"`
	DepositLiabilities                  *float64 `json:"deposit_liabilities,omitempty"`
	NonCurrentLiabilities               *float64 `json:"non_current_liabilities,omitempty"`
	NonCurrentDebt                      *float64 `json:"non_current_debt,omitempty"`
	TaxLiabilities                      *float64 `json:"tax_liabilities,omitempty"`
	ShareholdersEquity                  *float64 `json:"shareholders_equity,omitempty"`
	RetainedEarnings                    *float64 `json:"retained_earnings,omitempty"`
	AccumulatedOtherComprehensiveIncome *float64 `json:"accumulated_other_comprehensive_income,omitempty"`
	TotalDebt                           *float64 `json:"total_debt,omitempty"`
}

// CashFlowStatement mirrors the Financial Datasets CashFlowStatement schema.
type CashFlowStatement struct {
	Ticker                              *string  `json:"ticker,omitempty"`
	ReportPeriod                        *string  `json:"report_period,omitempty"`
	FiscalPeriod                        *string  `json:"fiscal_period,omitempty"`
	Period                              *string  `json:"period,omitempty"`
	Currency                            *string  `json:"currency,omitempty"`
	AccessionNumber                     *string  `json:"accession_number,omitempty"`
	FormType                            *string  `json:"form_type,omitempty"`
	FilingURL                           *string  `json:"filing_url,omitempty"`
	FilingDate                          *string  `json:"filing_date,omitempty"`
	FilingDatetime                      *string  `json:"filing_datetime,omitempty"`
	NetIncome                           *float64 `json:"net_income,omitempty"`
	DepreciationAndAmortization         *float64 `json:"depreciation_and_amortization,omitempty"`
	ShareBasedCompensation              *float64 `json:"share_based_compensation,omitempty"`
	NetCashFlowFromOperations           *float64 `json:"net_cash_flow_from_operations,omitempty"`
	CapitalExpenditure                  *float64 `json:"capital_expenditure,omitempty"`
	BusinessAcquisitionsAndDisposals    *float64 `json:"business_acquisitions_and_disposals,omitempty"`
	InvestmentAcquisitionsAndDisposals  *float64 `json:"investment_acquisitions_and_disposals,omitempty"`
	NetCashFlowFromInvesting            *float64 `json:"net_cash_flow_from_investing,omitempty"`
	IssuanceOrRepaymentOfDebtSecurities *float64 `json:"issuance_or_repayment_of_debt_securities,omitempty"`
	IssuanceOrPurchaseOfEquityShares    *float64 `json:"issuance_or_purchase_of_equity_shares,omitempty"`
	DividendsAndOtherCashDistributions  *float64 `json:"dividends_and_other_cash_distributions,omitempty"`
	NetCashFlowFromFinancing            *float64 `json:"net_cash_flow_from_financing,omitempty"`
	ChangeInCashAndEquivalents          *float64 `json:"change_in_cash_and_equivalents,omitempty"`
	EffectOfExchangeRateChanges         *float64 `json:"effect_of_exchange_rate_changes,omitempty"`
	EndingCashBalance                   *float64 `json:"ending_cash_balance,omitempty"`
	FreeCashFlow                        *float64 `json:"free_cash_flow,omitempty"`
}

// Filing mirrors the Financial Datasets Filing schema.
type Filing struct {
	CIK             *int64  `json:"cik,omitempty"`
	AccessionNumber *string `json:"accession_number,omitempty"`
	FilingType      *string `json:"filing_type,omitempty"`
	ReportDate      *string `json:"report_date,omitempty"`
	FilingDate      *string `json:"filing_date,omitempty"`
	Ticker          *string `json:"ticker,omitempty"`
	URL             *string `json:"url,omitempty"`
}

// FilingItem mirrors the Financial Datasets FilingItem schema.
type FilingItem struct {
	Number   *string           `json:"number,omitempty"`
	Name     *string           `json:"name,omitempty"`
	Text     *string           `json:"text,omitempty"`
	Exhibits []json.RawMessage `json:"exhibits,omitempty"`
}

// Price mirrors the Financial Datasets Price schema.
type Price struct {
	Open   *float64 `json:"open,omitempty"`
	Close  *float64 `json:"close,omitempty"`
	High   *float64 `json:"high,omitempty"`
	Low    *float64 `json:"low,omitempty"`
	Volume *int64   `json:"volume,omitempty"`
	Time   *string  `json:"time,omitempty"`
}

// PriceSnapshot mirrors the Financial Datasets PriceSnapshot schema.
type PriceSnapshot struct {
	Price            *float64 `json:"price,omitempty"`
	Ticker           *string  `json:"ticker,omitempty"`
	DayChange        *float64 `json:"day_change,omitempty"`
	DayChangePercent *float64 `json:"day_change_percent,omitempty"`
	Time             *string  `json:"time,omitempty"`
	TimeMilliseconds *float64 `json:"time_milliseconds,omitempty"`
}

// News mirrors the Financial Datasets News schema.
type News struct {
	Ticker *string `json:"ticker,omitempty"`
	Title  *string `json:"title,omitempty"`
	Source *string `json:"source,omitempty"`
	Date   *string `json:"date,omitempty"`
	URL    *string `json:"url,omitempty"`
}

// FinancialMetricSnapshot mirrors the Financial Datasets FinancialMetricSnapshot schema.
type FinancialMetricSnapshot struct {
	Ticker                        *string  `json:"ticker,omitempty"`
	Currency                      *string  `json:"currency,omitempty"`
	MarketCap                     *float64 `json:"market_cap,omitempty"`
	EnterpriseValue               *float64 `json:"enterprise_value,omitempty"`
	PriceToEarningsRatio          *float64 `json:"price_to_earnings_ratio,omitempty"`
	PriceToBookRatio              *float64 `json:"price_to_book_ratio,omitempty"`
	PriceToSalesRatio             *float64 `json:"price_to_sales_ratio,omitempty"`
	EnterpriseValueToEBITDARatio  *float64 `json:"enterprise_value_to_ebitda_ratio,omitempty"`
	EnterpriseValueToRevenueRatio *float64 `json:"enterprise_value_to_revenue_ratio,omitempty"`
	FreeCashFlowYield             *float64 `json:"free_cash_flow_yield,omitempty"`
	PegRatio                      *float64 `json:"peg_ratio,omitempty"`
	GrossMargin                   *float64 `json:"gross_margin,omitempty"`
	OperatingMargin               *float64 `json:"operating_margin,omitempty"`
	NetMargin                     *float64 `json:"net_margin,omitempty"`
	ReturnOnEquity                *float64 `json:"return_on_equity,omitempty"`
	ReturnOnAssets                *float64 `json:"return_on_assets,omitempty"`
	ReturnOnInvestedCapital       *float64 `json:"return_on_invested_capital,omitempty"`
	AssetTurnover                 *float64 `json:"asset_turnover,omitempty"`
	InventoryTurnover             *float64 `json:"inventory_turnover,omitempty"`
	ReceivablesTurnover           *float64 `json:"receivables_turnover,omitempty"`
	DaysSalesOutstanding          *float64 `json:"days_sales_outstanding,omitempty"`
	OperatingCycle                *float64 `json:"operating_cycle,omitempty"`
	WorkingCapitalTurnover        *float64 `json:"working_capital_turnover,omitempty"`
	CurrentRatio                  *float64 `json:"current_ratio,omitempty"`
	QuickRatio                    *float64 `json:"quick_ratio,omitempty"`
	CashRatio                     *float64 `json:"cash_ratio,omitempty"`
	OperatingCashFlowRatio        *float64 `json:"operating_cash_flow_ratio,omitempty"`
	DebtToEquity                  *float64 `json:"debt_to_equity,omitempty"`
	DebtToAssets                  *float64 `json:"debt_to_assets,omitempty"`
	InterestCoverage              *float64 `json:"interest_coverage,omitempty"`
	RevenueGrowth                 *float64 `json:"revenue_growth,omitempty"`
	EarningsGrowth                *float64 `json:"earnings_growth,omitempty"`
	BookValueGrowth               *float64 `json:"book_value_growth,omitempty"`
	EarningsPerShareGrowth        *float64 `json:"earnings_per_share_growth,omitempty"`
	FreeCashFlowGrowth            *float64 `json:"free_cash_flow_growth,omitempty"`
	OperatingIncomeGrowth         *float64 `json:"operating_income_growth,omitempty"`
	EBITDAGrowth                  *float64 `json:"ebitda_growth,omitempty"`
	PayoutRatio                   *float64 `json:"payout_ratio,omitempty"`
	EarningsPerShare              *float64 `json:"earnings_per_share,omitempty"`
	BookValuePerShare             *float64 `json:"book_value_per_share,omitempty"`
	FreeCashFlowPerShare          *float64 `json:"free_cash_flow_per_share,omitempty"`
}

// EarningsRecord mirrors the Financial Datasets EarningsRecord schema.
type EarningsRecord struct {
	Ticker          *string           `json:"ticker,omitempty"`
	ReportPeriod    *string           `json:"report_period,omitempty"`
	FiscalPeriod    *string           `json:"fiscal_period,omitempty"`
	Currency        *string           `json:"currency,omitempty"`
	SourceType      *string           `json:"source_type,omitempty"`
	FilingDate      *string           `json:"filing_date,omitempty"`
	FilingDatetime  *string           `json:"filing_datetime,omitempty"`
	FilingWindow    *string           `json:"filing_window,omitempty"`
	Signals         []json.RawMessage `json:"signals,omitempty"`
	FilingURL       *string           `json:"filing_url,omitempty"`
	AccessionNumber *string           `json:"accession_number,omitempty"`
	Quarterly       json.RawMessage   `json:"quarterly,omitempty"`
	Annual          json.RawMessage   `json:"annual,omitempty"`
}

// InsiderTrade mirrors the Financial Datasets InsiderTrade schema.
type InsiderTrade struct {
	Ticker                       *string  `json:"ticker,omitempty"`
	Issuer                       *string  `json:"issuer,omitempty"`
	Name                         *string  `json:"name,omitempty"`
	Title                        *string  `json:"title,omitempty"`
	IsBoardDirector              *bool    `json:"is_board_director,omitempty"`
	FormType                     *string  `json:"form_type,omitempty"`
	FilingDate                   *string  `json:"filing_date,omitempty"`
	ReportPeriod                 *string  `json:"report_period,omitempty"`
	TransactionDate              *string  `json:"transaction_date,omitempty"`
	TransactionCode              *string  `json:"transaction_code,omitempty"`
	TransactionType              *string  `json:"transaction_type,omitempty"`
	TransactionShares            *float64 `json:"transaction_shares,omitempty"`
	TransactionPricePerShare     *float64 `json:"transaction_price_per_share,omitempty"`
	TransactionValue             *float64 `json:"transaction_value,omitempty"`
	SharesOwnedBeforeTransaction *float64 `json:"shares_owned_before_transaction,omitempty"`
	SharesOwnedAfterTransaction  *float64 `json:"shares_owned_after_transaction,omitempty"`
	SecurityTitle                *string  `json:"security_title,omitempty"`
}

// InterestRate mirrors the Financial Datasets InterestRate schema.
type InterestRate struct {
	Bank *string  `json:"bank,omitempty"`
	Name *string  `json:"name,omitempty"`
	Rate *float64 `json:"rate,omitempty"`
	Date *string  `json:"date,omitempty"`
}

// FundHolding mirrors the Financial Datasets FundHolding schema.
type FundHolding struct {
	Ticker      *string  `json:"ticker,omitempty"`
	Name        *string  `json:"name,omitempty"`
	CUSIP       *string  `json:"cusip,omitempty"`
	ISIN        *string  `json:"isin,omitempty"`
	Weight      *float64 `json:"weight,omitempty"`
	MarketValue *float64 `json:"market_value,omitempty"`
	Shares      *float64 `json:"shares,omitempty"`
	AssetClass  *string  `json:"asset_class,omitempty"`
}

// InstitutionalHolding mirrors the Financial Datasets InstitutionalHolding schema.
type InstitutionalHolding struct {
	Ticker          *string           `json:"ticker,omitempty"`
	NameOfIssuer    *string           `json:"name_of_issuer,omitempty"`
	CUSIP           *string           `json:"cusip,omitempty"`
	ReportPeriod    *string           `json:"report_period,omitempty"`
	FilingDate      *string           `json:"filing_date,omitempty"`
	FormType        *string           `json:"form_type,omitempty"`
	AccessionNumber *string           `json:"accession_number,omitempty"`
	TitleOfClass    *string           `json:"title_of_class,omitempty"`
	PutCall         *string           `json:"put_call,omitempty"`
	Shares          *int64            `json:"shares,omitempty"`
	ValueUSD        *int64            `json:"value_usd,omitempty"`
	ReportedPrice   *float64          `json:"reported_price,omitempty"`
	FilerCIK        *string           `json:"filer_cik,omitempty"`
	FilerName       *string           `json:"filer_name,omitempty"`
	Subsidiaries    []json.RawMessage `json:"subsidiaries,omitempty"`
}

// KPIMetric mirrors the Financial Datasets KPIMetric schema.
type KPIMetric struct {
	Ticker       *string  `json:"ticker,omitempty"`
	MetricName   *string  `json:"metric_name,omitempty"`
	Value        *float64 `json:"value,omitempty"`
	Unit         *string  `json:"unit,omitempty"`
	Period       *string  `json:"period,omitempty"`
	PeriodType   *string  `json:"period_type,omitempty"`
	Segment      *string  `json:"segment,omitempty"`
	YOYValue     *float64 `json:"yoy_value,omitempty"`
	YOYChangePCT *float64 `json:"yoy_change_pct,omitempty"`
	SourceText   *string  `json:"source_text,omitempty"`
	SourceURL    *string  `json:"source_url,omitempty"`
}

// KPIGuidanceItem mirrors the Financial Datasets KPIGuidanceItem schema.
type KPIGuidanceItem struct {
	Ticker          *string  `json:"ticker,omitempty"`
	MetricName      *string  `json:"metric_name,omitempty"`
	Value           *float64 `json:"value,omitempty"`
	Unit            *string  `json:"unit,omitempty"`
	Period          *string  `json:"period,omitempty"`
	PeriodType      *string  `json:"period_type,omitempty"`
	Segment         *string  `json:"segment,omitempty"`
	Low             *float64 `json:"low,omitempty"`
	High            *float64 `json:"high,omitempty"`
	PointEstimate   *float64 `json:"point_estimate,omitempty"`
	PriorValue      *float64 `json:"prior_value,omitempty"`
	RawText         *string  `json:"raw_text,omitempty"`
	ChangeDirection *string  `json:"change_direction,omitempty"`
	SourceText      *string  `json:"source_text,omitempty"`
	SourceURL       *string  `json:"source_url,omitempty"`
}

// KPINonGAAPMetric mirrors the Financial Datasets KPINonGAAPMetric schema.
type KPINonGAAPMetric struct {
	Ticker         *string  `json:"ticker,omitempty"`
	MetricName     *string  `json:"metric_name,omitempty"`
	Value          *float64 `json:"value,omitempty"`
	Unit           *string  `json:"unit,omitempty"`
	Period         *string  `json:"period,omitempty"`
	PeriodType     *string  `json:"period_type,omitempty"`
	GAAPEquivalent *string  `json:"gaap_equivalent,omitempty"`
	KeyAdjustments *string  `json:"key_adjustments,omitempty"`
	SourceText     *string  `json:"source_text,omitempty"`
	SourceURL      *string  `json:"source_url,omitempty"`
}

// SegmentBreakdown mirrors the Financial Datasets SegmentBreakdown schema.
type SegmentBreakdown struct {
	Label *string  `json:"label,omitempty"`
	Value *float64 `json:"value,omitempty"`
}

// SegmentMetadata mirrors the Financial Datasets SegmentMetadata schema.
type SegmentMetadata struct {
	Ticker          *string `json:"ticker,omitempty"`
	ReportPeriod    *string `json:"report_period,omitempty"`
	FiscalPeriod    *string `json:"fiscal_period,omitempty"`
	Period          *string `json:"period,omitempty"`
	Currency        *string `json:"currency,omitempty"`
	AccessionNumber *string `json:"accession_number,omitempty"`
	FilingURL       *string `json:"filing_url,omitempty"`
}

// ErrorResponse mirrors the Financial Datasets ErrorResponse schema.
type ErrorResponse struct {
	Error   *string `json:"error,omitempty"`
	Message *string `json:"message,omitempty"`
}
