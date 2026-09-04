package providers

import (
	"encoding/json"
	"testing"
)

func gateBFilings() []RawFiling {
	return []RawFiling{
		{FilingDate: "2026-01-20", ReportDate: "2025-12-31", Form: "10-Q", PrimaryDocumentURL: "https://www.sec.gov/Archives/edgar/data/320193/000032019326000001/aapl-20251231.htm"},
		{FilingDate: "2026-01-10", ReportDate: "2025-12-31", Form: "10-K", PrimaryDocumentURL: "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20251231.htm"},
		{FilingDate: "2025-11-01", ReportDate: "2025-09-30", Form: "10-Q", PrimaryDocumentURL: "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250930.htm"},
		{FilingDate: "2025-10-31", ReportDate: "2025-09-30", Form: "10-K", PrimaryDocumentURL: "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20250930.htm"},
		{FilingDate: "2025-01-15", ReportDate: "2024-12-31", Form: "10-K", PrimaryDocumentURL: "https://www.sec.gov/Archives/edgar/data/320193/000032019324000123/aapl-20241231.htm"},
	}
}

func decodeTimeDimension(t *testing.T, raw json.RawMessage) EarningsTimeDimension {
	t.Helper()
	var block EarningsTimeDimension
	if err := json.Unmarshal(raw, &block); err != nil {
		t.Fatalf("decode EarningsTimeDimension: %v", err)
	}
	return block
}

func TestNormalizeEarnings_ComposesRequiredFields(t *testing.T) {
	value := parseFixture(t, gateBStatementsFixtureJSON)

	data, err := NormalizeEarnings(value, gateBFilings(), "AAPL", 2)
	if err != nil {
		t.Fatalf("NormalizeEarnings: %v", err)
	}
	if len(data.Records) != 2 {
		t.Fatalf("want 2 records, got %d", len(data.Records))
	}
	newest := data.Records[0]

	requireStr(t, "ticker", newest.Ticker, "AAPL")
	requireStr(t, "report_period", newest.ReportPeriod, "2025-12-31")
	requireStr(t, "fiscal_period", newest.FiscalPeriod, "2025-Q4")
	requireStr(t, "source_type", newest.SourceType, "10-Q")
	requireStr(t, "filing_date", newest.FilingDate, "2026-01-20")
	if newest.FilingURL == nil || len(*newest.FilingURL) < len("https://www.sec.gov/Archives/") ||
		(*newest.FilingURL)[:len("https://www.sec.gov/Archives/")] != "https://www.sec.gov/Archives/" {
		t.Fatalf("filing_url = %v, want an sec.gov/Archives URL", newest.FilingURL)
	}
	requireStr(t, "accession_number", newest.AccessionNumber, "0000320193-26-000001")

	for _, required := range []struct {
		name string
		val  *string
	}{
		{"ticker", newest.Ticker},
		{"report_period", newest.ReportPeriod},
		{"source_type", newest.SourceType},
		{"filing_date", newest.FilingDate},
		{"filing_url", newest.FilingURL},
		{"accession_number", newest.AccessionNumber},
	} {
		if required.val == nil {
			t.Errorf("required field %s is missing", required.name)
		}
	}

	if newest.Quarterly == nil {
		t.Fatal("want a quarterly block")
	}
	quarterly := decodeTimeDimension(t, newest.Quarterly)
	requireFloat(t, "quarterly.revenue", quarterly.Revenue, 80)
	requireFloat(t, "quarterly.revenue_chg", quarterly.RevenueChg, (80.0-60.0)/60.0)        // decimal ratio qoq
	requireFloat(t, "quarterly.revenue_yoy_chg", quarterly.RevenueYoYChg, (80.0-40.0)/40.0) // decimal ratio yoy
	requireFloat(t, "quarterly.earnings_per_share", quarterly.EarningsPerShare, 0.8)
	requireFloat(t, "quarterly.gross_margin", quarterly.GrossMargin, 0.4)
	if quarterly.GrossMarginChgBps == nil {
		t.Error("want quarterly.gross_margin_chg_bps present")
	}
	requireFloat(t, "quarterly.total_assets", quarterly.TotalAssets, 170)
	requireFloat(t, "quarterly.free_cash_flow", quarterly.FreeCashFlow, 9.6)

	if newest.Annual != nil {
		t.Error("want no annual block on a 10-Q record")
	}
}

func TestNormalizeEarnings_TenKIncludesAnnualBlock(t *testing.T) {
	value := parseFixture(t, gateBStatementsFixtureJSON)

	data, err := NormalizeEarnings(value, gateBFilings(), "AAPL", 4)
	if err != nil {
		t.Fatalf("NormalizeEarnings: %v", err)
	}

	var tenKWithAnnual *int
	for i, record := range data.Records {
		if record.SourceType != nil && *record.SourceType == "10-K" && record.Annual != nil {
			idx := i
			tenKWithAnnual = &idx
			break
		}
	}
	if tenKWithAnnual == nil {
		t.Fatal("want a 10-K record with an annual block")
	}
	record := data.Records[*tenKWithAnnual]
	requireStr(t, "report_period", record.ReportPeriod, "2025-12-31")
	requireStr(t, "filing_date", record.FilingDate, "2026-01-10")

	annual := decodeTimeDimension(t, record.Annual)
	requireFloat(t, "annual.revenue", annual.Revenue, 200)
	requireFloat(t, "annual.revenue_chg", annual.RevenueChg, (200.0-100.0)/100.0) // yoy ratio in annual payload
	if annual.RevenueYoYChg != nil {
		t.Error("annual.revenue_yoy_chg present, want omitted: yoy fields are quarterly-only")
	}

	quarterly := decodeTimeDimension(t, record.Quarterly)
	requireFloat(t, "quarterly.revenue", quarterly.Revenue, 80)
	requireFloat(t, "quarterly.revenue_chg", quarterly.RevenueChg, (80.0-60.0)/60.0)
	requireFloat(t, "quarterly.revenue_yoy_chg", quarterly.RevenueYoYChg, (80.0-40.0)/40.0)
}

func TestNormalizeEarnings_NoMatchingFilingsIsSchemaDrift(t *testing.T) {
	value := parseFixture(t, gateBStatementsFixtureJSON)
	_, err := NormalizeEarnings(value, []RawFiling{{Form: "8-K", ReportDate: "2025-12-31", FilingDate: "2026-01-01", PrimaryDocumentURL: "https://www.sec.gov/Archives/edgar/data/1/000000000000000001a.htm"}}, "AAPL", 10)
	if err == nil {
		t.Fatal("want SchemaDriftError when no 10-K/10-Q rows are present")
	}
	if _, ok := err.(*SchemaDriftError); !ok {
		t.Fatalf("want *SchemaDriftError, got %T", err)
	}
}
