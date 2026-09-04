package providers

import (
	"testing"
)

// gateBStatementsFixtureJSON ports statement_fixture() from tests/test_gate_b.py.
const gateBStatementsFixtureJSON = `{"incomeStatement":{"labels":["Revenue","Cost of Revenue","Gross Profit","Operating Income","Net Income","EPS (Diluted)","EPS (Basic)","Shares Outstanding (Basic)","Shares Outstanding (Diluted)","EBIT","EBITDA"],"children":{"annual":{"Non-Operating Items":{"labels":["Non-Operating Interest Expense"]}},"quarterly":{"Non-Operating Items":{"labels":["Non-Operating Interest Expense"]}}},"quarterly":{"periodEnding":["2024-03-31","2024-06-30","2024-09-30","2024-12-31","2025-03-31","2025-06-30","2025-09-30","2025-12-31","2026-03-31"],"values":[[10,20,30,40,20,40,60,80,40],[6.0,12.0,18.0,24.0,12.0,24.0,36.0,48.0,24.0],[4.0,8.0,12.0,16.0,8.0,16.0,24.0,32.0,16.0],[2.0,4.0,6.0,8.0,4.0,8.0,12.0,16.0,8.0],[1.0,2.0,3.0,4.0,2.0,4.0,6.0,8.0,4.0],[0.1,0.2,0.3,0.4,0.2,0.4,0.6,0.8,0.4],[0.1,0.2,0.3,0.4,0.2,0.4,0.6,0.8,0.4],[10.0,10.0,10.0,10.0,10.0,10.0,10.0,10.0,10.0],[11.0,11.0,11.0,11.0,11.0,11.0,11.0,11.0,11.0],[2.5,5.0,7.5,10.0,5.0,10.0,15.0,20.0,10.0],[3.0,6.0,9.0,12.0,6.0,12.0,18.0,24.0,12.0]],"children":{"Non-Operating Items":{"values":[[0.25,0.5,0.75,1.0,0.5,1.0,1.5,2.0,1.0]]}}},"annual":{"periodEnding":["2024-12-31","2025-12-31"],"values":[[100,200],[60.0,120.0],[40.0,80.0],[20.0,40.0],[10.0,20.0],[1.0,2.0],[1.0,2.0],[10.0,10.0],[11.0,11.0],[25.0,50.0],[30.0,60.0]],"children":{"Non-Operating Items":{"values":[[2.5,5.0]]}}}},"balanceSheet":{"labels":["Total Current Assets","Total Current Liabilities","Total Assets","Total Liabilities","Total Shareholders Equity"],"children":{"annual":{"Total Current Assets":{"labels":["Cash and Cash Equivalents","Accounts Receivable","Inventory"]},"Total Current Liabilities":{"labels":["Short-Term Debt"]},"Total Non-Current Liabilities":{"labels":["Long-Term Debt"]}},"quarterly":{"Total Current Assets":{"labels":["Cash and Cash Equivalents","Accounts Receivable","Inventory"]},"Total Current Liabilities":{"labels":["Short-Term Debt"]},"Total Non-Current Liabilities":{"labels":["Long-Term Debt"]}}},"quarterly":{"periodEnding":["2024-03-31","2024-06-30","2024-09-30","2024-12-31","2025-03-31","2025-06-30","2025-09-30","2025-12-31","2026-03-31"],"values":[[40,42,44,46,48,50,52,54,56],[20,21,22,23,24,25,26,27,28],[100,110,120,130,140,150,160,170,180],[50,55,60,65,70,75,80,85,90],[50,55,60,65,70,75,80,85,90]],"children":{"Total Current Assets":{"values":[[10.0,10.5,11.0,11.5,12.0,12.5,13.0,13.5,14.0],[10,11,12,13,14,15,16,17,18],[5,6,7,8,9,10,11,12,13]]},"Total Current Liabilities":{"values":[[3,3,3,3,3,3,3,3,3]]},"Total Non-Current Liabilities":{"values":[[7,7,7,7,7,7,7,7,7]]}}},"annual":{"periodEnding":["2024-12-31","2025-12-31"],"values":[[40,42],[20,21],[100,110],[50,55],[50,55]],"children":{"Total Current Assets":{"values":[[10.0,10.5],[10,11],[5,6]]},"Total Current Liabilities":{"values":[[3,3]]},"Total Non-Current Liabilities":{"values":[[7,7]]}}}},"cashflow":{"labels":["Cash Flow from Operating Activities","Cash Flow from Investing Activities","Cash Flow from Financing Activities","Free Cash Flow","Net Cash Flow"],"children":{"annual":{"Cash Flow from Investing Activities":{"labels":["Capital Expenditure"]},"Cash Flow from Financing Activities":{"labels":["Common Dividends"]}},"quarterly":{"Cash Flow from Investing Activities":{"labels":["Capital Expenditure"]},"Cash Flow from Financing Activities":{"labels":["Common Dividends"]}}},"quarterly":{"periodEnding":["2024-03-31","2024-06-30","2024-09-30","2024-12-31","2025-03-31","2025-06-30","2025-09-30","2025-12-31","2026-03-31"],"values":[[1.5,3.0,4.5,6.0,3.0,6.0,9.0,12.0,6.0],[-0.5,-1.0,-1.5,-2.0,-1.0,-2.0,-3.0,-4.0,-2.0],[-0.3,-0.6,-0.8999999999999999,-1.2,-0.6,-1.2,-1.7999999999999998,-2.4,-1.2],[1.2,2.4,3.5999999999999996,4.8,2.4,4.8,7.199999999999999,9.6,4.8],[0.7000000000000001,1.4000000000000001,2.1,2.8000000000000003,1.4000000000000001,2.8000000000000003,4.2,5.6000000000000005,2.8000000000000003]],"children":{"Cash Flow from Investing Activities":{"values":[[-0.5,-1.0,-1.5,-2.0,-1.0,-2.0,-3.0,-4.0,-2.0]]},"Cash Flow from Financing Activities":{"values":[[-0.2,-0.4,-0.6,-0.8,-0.4,-0.8,-1.2,-1.6,-0.8]]}}},"annual":{"periodEnding":["2024-12-31","2025-12-31"],"values":[[15.0,30.0],[-5.0,-10.0],[-3.0,-6.0],[12.0,24.0],[7.000000000000001,14.000000000000002]],"children":{"Cash Flow from Investing Activities":{"values":[[-5.0,-10.0]]},"Cash Flow from Financing Activities":{"values":[[-2.0,-4.0]]}}}}}`

func TestNormalizeFinancialMetrics_Annual(t *testing.T) {
	value := parseFixture(t, gateBStatementsFixtureJSON)

	data, err := NormalizeFinancialMetrics(value, "AAPL", PeriodAnnual, 4, MetricsFilters{})
	if err != nil {
		t.Fatalf("NormalizeFinancialMetrics: %v", err)
	}
	if len(data.Records) != 2 {
		t.Fatalf("want 2 annual records, got %d", len(data.Records))
	}
	newest := data.Records[0]

	requireStr(t, "report_period", newest.ReportPeriod, "2025-12-31")
	requireStr(t, "fiscal_period", newest.FiscalPeriod, "FY2025")
	requireStr(t, "period", newest.Period, "annual")

	requireFloat(t, "gross_margin", newest.GrossMargin, 0.4)
	requireFloat(t, "operating_margin", newest.OperatingMargin, 0.2)
	requireFloat(t, "net_margin", newest.NetMargin, 0.1)
	requireFloat(t, "return_on_equity", newest.ReturnOnEquity, 20.0/55.0)
	requireFloat(t, "current_ratio", newest.CurrentRatio, 2.0)
	requireFloat(t, "debt_to_equity", newest.DebtToEquity, 10.0/55.0)
	requireFloat(t, "interest_coverage", newest.InterestCoverage, 0.25/0.025)
	requireFloat(t, "revenue_growth", newest.RevenueGrowth, 1.0)
	requireFloat(t, "earnings_per_share", newest.EarningsPerShare, 2.0)
	requireFloat(t, "book_value_per_share", newest.BookValuePerShare, 5.5)

	// Filing identity (accession_number, form_type, filing_url, filing_date) and
	// valuation fields are joined at a higher service layer, out of scope for
	// this port; financial_metrics.py's own _metric_record never sets them.
}

func TestNormalizeFinancialMetrics_TTMOmitsFiscalPeriod(t *testing.T) {
	value := parseFixture(t, gateBStatementsFixtureJSON)

	data, err := NormalizeFinancialMetrics(value, "AAPL", PeriodTTM, 10, MetricsFilters{})
	if err != nil {
		t.Fatalf("NormalizeFinancialMetrics: %v", err)
	}
	if len(data.Records) == 0 {
		t.Fatal("want at least one TTM record")
	}
	for _, record := range data.Records {
		if record.FiscalPeriod != nil {
			t.Errorf("TTM record has fiscal_period %v, want omitted", *record.FiscalPeriod)
		}
		requireStr(t, "period", record.Period, "ttm")
	}
}

func TestNormalizeFinancialMetrics_ReportPeriodFilter(t *testing.T) {
	value := parseFixture(t, gateBStatementsFixtureJSON)
	exact := mustDate(t, "2024-12-31")

	data, err := NormalizeFinancialMetrics(value, "AAPL", PeriodAnnual, 10, MetricsFilters{Exact: &exact})
	if err != nil {
		t.Fatalf("NormalizeFinancialMetrics: %v", err)
	}
	if len(data.Records) != 1 {
		t.Fatalf("want 1 record, got %d", len(data.Records))
	}
	requireStr(t, "report_period", data.Records[0].ReportPeriod, "2024-12-31")
}

func requireStr(t *testing.T, name string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil, want %q", name, want)
	}
	if *got != want {
		t.Errorf("%s = %q, want %q", name, *got, want)
	}
}

func requireFloat(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil, want %v", name, want)
	}
	const epsilon = 1e-9
	diff := *got - want
	if diff < -epsilon || diff > epsilon {
		t.Errorf("%s = %v, want %v", name, *got, want)
	}
}
