package service

import "testing"

// The 13F feed labels rows with the quarter and the submission moment.
// report_period must be the quarter end and filing_date the submission
// day; the filer CIK must carry the SEC's ten-digit padding.
func TestInstitutionalHoldings_QuarterEndAndFilingDate(t *testing.T) {
	row := map[string]any{
		"quarter":          "2026Q2",
		"report_date_time": "2026-08-07<BR>12:29:42",
		"holder":           `<a href="/portfolio-holdings/1364742.html">BlackRock, Inc.</a>`,
	}
	if end := instHoldingsQuarterEnd(row); end == nil || end.Format(dateLayout) != "2026-06-30" {
		t.Fatalf("quarter end = %v, want 2026-06-30", end)
	}
	if filed := instHoldingsFirstDate(row, "report_date_time"); filed == nil || filed.Format(dateLayout) != "2026-08-07" {
		t.Fatalf("filing date = %v, want 2026-08-07", filed)
	}
	if cik := holderCIK(row["holder"].(string)); cik == nil || *cik != "0001364742" {
		t.Fatalf("filer cik = %v, want 0001364742", cik)
	}
	if got := instHoldingsQuarterEnd(map[string]any{"quarter": "Q2 2026"}); got != nil {
		t.Fatalf("an unrecognised quarter label must yield nil, got %v", got)
	}
}
