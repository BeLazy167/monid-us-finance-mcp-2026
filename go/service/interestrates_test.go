package service

import (
	"os"
	"testing"
)

// Excerpts of the four pages as Context.dev returned them on 2026-09-04.
// The Fed and ECB publish tables, the BOE prose under a heading, and the
// BOJ page answered "Network Busy" that day, which must parse as nothing
// rather than as a rate.
func TestParsePolicyRate(t *testing.T) {
	for _, tc := range []struct {
		bank, file, date string
		rate             float64
		found            bool
	}{
		{bank: "FED", file: "fed.md", rate: 3.625, date: "2025-12-11", found: true},
		{bank: "ECB", file: "ecb.md", rate: 2.25, date: "2026-06-17", found: true},
		{bank: "BOE", file: "boe.md", rate: 3.75, date: "2026-07-30", found: true},
		{bank: "BOJ", file: "boj_busy.md"},
	} {
		t.Run(tc.bank, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/rates/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			got := parsePolicyRate(string(raw), tc.bank)
			if !tc.found {
				if got != nil {
					t.Fatalf("parsed %+v from a page that carries no rate", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("no rate parsed")
			}
			if got.Rate != tc.rate {
				t.Fatalf("rate = %v, want %v", got.Rate, tc.rate)
			}
			if got.Date == nil || *got.Date != tc.date {
				t.Fatalf("date = %v, want %s", got.Date, tc.date)
			}
		})
	}
}

// The BOE page names the next meeting ("Next due: 17 September 2026")
// right after the current rate. That future date must never become the
// rate's date; the decision announced below it must.
func TestBOEDateIsTheDecisionNotTheNextMeeting(t *testing.T) {
	page := "## Current Bank Rate 3.75%\n\nNext due: 17 September 2026\n\n" +
		"30 July 2026\n\n### Bank Rate maintained at 3.75% - July 2026\n"
	got := parsePolicyRate(page, "BOE")
	if got == nil || got.Date == nil || *got.Date != "2026-07-30" {
		t.Fatalf("got %+v, want date 2026-07-30", got)
	}
}
