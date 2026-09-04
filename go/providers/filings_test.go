package providers

import (
	"encoding/json"
	"testing"
)

func TestValidateFilingTypes(t *testing.T) {
	t.Run("nil means no filter", func(t *testing.T) {
		got, err := ValidateFilingTypes(nil)
		if err != nil || got != nil {
			t.Fatalf("got %v, %v; want nil, nil", got, err)
		}
	})
	t.Run("normalizes case and trims", func(t *testing.T) {
		got, err := ValidateFilingTypes([]string{" 10-k ", "8-K"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{"10-K": true, "8-K": true}
		if len(got) != len(want) || !got["10-K"] || !got["8-K"] {
			t.Fatalf("got %v, want %v", got, want)
		}
	})
	t.Run("rejects values outside the FD enum", func(t *testing.T) {
		_, err := ValidateFilingTypes([]string{"40-F"})
		if err == nil {
			t.Fatal("expected an error for an unsupported filing type")
		}
	})
	t.Run("accepts every FD enum member", func(t *testing.T) {
		for _, ft := range []string{"10-K", "10-Q", "8-K", "20-F", "6-K"} {
			if _, err := ValidateFilingTypes([]string{ft}); err != nil {
				t.Fatalf("%s should be valid: %v", ft, err)
			}
		}
	})
}

// filingsFixture mirrors tests/test_service.py's FILINGS constant.
const filingsFixture = `[
	{
		"filingDate": "2026-02-01",
		"reportDate": "2025-12-31",
		"form": "10-K",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20241231.htm"
	},
	{
		"filingDate": "2026-01-15",
		"reportDate": "2025-12-31",
		"form": "8-K",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019326000001/a.htm"
	},
	{
		"filingDate": "2025-11-01",
		"reportDate": "2025-09-30",
		"form": "10-Q",
		"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/data/320193/000032019325000010/aapl-20250930.htm"
	}
]`

// TestNormalizeFilings_FilterAndShape ports
// tests/test_service.py::test_filings_filter_and_shape.
func TestNormalizeFilings_FilterAndShape(t *testing.T) {
	filingTypes, err := ValidateFilingTypes([]string{"10-K"})
	if err != nil {
		t.Fatalf("ValidateFilingTypes: %v", err)
	}
	records, err := NormalizeFilings(json.RawMessage(filingsFixture), "AAPL", filingTypes, 10, nil, nil)
	if err != nil {
		t.Fatalf("NormalizeFilings: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]
	assertStrPtr(t, "filing_type", record.FilingType, "10-K")
	assertStrPtr(t, "report_date", record.ReportDate, "2025-12-31")
	assertStrPtr(t, "filing_date", record.FilingDate, "2026-02-01")
	assertStrPtr(t, "ticker", record.Ticker, "AAPL")
	assertStrPtr(t, "accession_number", record.AccessionNumber, "0000320193-25-000079")
	if record.CIK != nil {
		t.Fatalf("cik should stay unsourced, got %v", *record.CIK)
	}
}

// TestNormalizeFilings_PaginationWalk ports
// tests/test_service.py::test_filings_pagination_walk (the provider half:
// 14 same-report-date records truncate to the requested limit).
func TestNormalizeFilings_PaginationWalk(t *testing.T) {
	rows := make([]map[string]any, 0, 14)
	for i := 0; i < 14; i++ {
		rows = append(rows, map[string]any{
			"filingDate":         "2026-01-01",
			"reportDate":         "2025-12-31",
			"form":               "8-K",
			"primaryDocumentUrl": "https://www.sec.gov/Archives/edgar/x.htm",
		})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	records, err := NormalizeFilings(json.RawMessage(raw), "AAPL", nil, 14, nil, nil)
	if err != nil {
		t.Fatalf("NormalizeFilings: %v", err)
	}
	if len(records) != 14 {
		t.Fatalf("got %d records, want 14", len(records))
	}
	limited, err := NormalizeFilings(json.RawMessage(raw), "AAPL", nil, 10, nil, nil)
	if err != nil {
		t.Fatalf("NormalizeFilings: %v", err)
	}
	if len(limited) != 10 {
		t.Fatalf("got %d records, want 10", len(limited))
	}
}

func TestNormalizeFilings_InvalidPayload(t *testing.T) {
	_, err := NormalizeFilings(json.RawMessage(`{"unexpected": 1}`), "AAPL", nil, 10, nil, nil)
	if err == nil {
		t.Fatal("expected a schema-drift error for a payload without a record list")
	}
}

func assertStrPtr(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil, want %q", field, want)
	}
	if *got != want {
		t.Fatalf("%s: got %q, want %q", field, *got, want)
	}
}
