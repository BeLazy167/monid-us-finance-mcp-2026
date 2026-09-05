package service

import (
	"os"
	"testing"
	"time"
)

// The pages already carry every decision; the parser must read all of
// them, not just the newest row.
func TestInterestRateHistory(t *testing.T) {
	for _, tc := range []struct {
		bank, file string
		min        int
		newestDate string
		newestRate float64
	}{
		{bank: "FED", file: "fed.md", min: 6, newestDate: "2025-12-11", newestRate: 3.625},
		{bank: "ECB", file: "ecb.md", min: 3, newestDate: "2026-06-17", newestRate: 2.25},
	} {
		t.Run(tc.bank, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/rates/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			h := interestRateHistory(string(raw), tc.bank, "Test Bank")
			if len(h) < tc.min {
				t.Fatalf("got %d decisions, want at least %d", len(h), tc.min)
			}
			if *h[0].Date != tc.newestDate || h[0].Rate != tc.newestRate {
				t.Fatalf("newest = %s %v, want %s %v", *h[0].Date, h[0].Rate, tc.newestDate, tc.newestRate)
			}
			for _, d := range h {
				if d.Bank != tc.bank || d.Name == "" || d.Date == nil {
					t.Fatalf("incomplete decision: %+v", d)
				}
			}
		})
	}
}

// Financial Datasets publishes the rate in force at the start of each
// month. Resampling the decisions must reproduce that, and must not
// invent a month that predates the first decision on the page.
func TestMonthlySeries(t *testing.T) {
	iso := func(s string) *string { return &s }
	history := []bankRate{
		{Bank: "FED", Name: "Federal Reserve", Rate: 3.625, Date: iso("2025-12-11")},
		{Bank: "FED", Name: "Federal Reserve", Rate: 3.875, Date: iso("2025-10-30")},
	}
	from := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got := monthlySeries(history, from, to)

	want := []struct {
		date string
		rate float64
	}{
		{"2025-11-01", 3.875}, // after the October decision
		{"2025-12-01", 3.875}, // the December cut lands on the 11th
		{"2026-01-01", 3.625},
		{"2026-02-01", 3.625},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d points, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if *got[i].Date != w.date || got[i].Rate != w.rate {
			t.Fatalf("point %d = %s %v, want %s %v", i, *got[i].Date, got[i].Rate, w.date, w.rate)
		}
	}
}
