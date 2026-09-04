package service

import (
	"os"
	"testing"
)

// Excerpts of the pages as Context.dev returned them on 2026-09-04: the
// Fed and ECB publish tables, the BOE a rate under a heading. A page that
// carries no rate (a "Network Busy" block page) must parse as nothing.
func TestParsePolicyRate(t *testing.T) {
	for _, tc := range []struct {
		bank, file, date string
		rate             float64
		found            bool
	}{
		{bank: "FED", file: "fed.md", rate: 3.625, date: "2025-12-11", found: true},
		{bank: "ECB", file: "ecb.md", rate: 2.25, date: "2026-06-17", found: true},
		{bank: "BOE", file: "boe.md", rate: 3.75, date: "2026-07-30", found: true},
		{bank: "BOE", file: "busy.md"},
	} {
		t.Run(tc.bank, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/rates/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			rate, date := parsePolicyRate(string(raw), tc.bank)
			if !tc.found {
				if rate != nil {
					t.Fatalf("parsed %v from a page that carries no rate", *rate)
				}
				return
			}
			if rate == nil {
				t.Fatalf("no rate parsed")
			}
			if *rate != tc.rate {
				t.Fatalf("rate = %v, want %v", *rate, tc.rate)
			}
			if date == nil || *date != tc.date {
				t.Fatalf("date = %v, want %s", date, tc.date)
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
	rate, date := parsePolicyRate(page, "BOE")
	if rate == nil || date == nil || *date != "2026-07-30" {
		t.Fatalf("got rate %v date %v, want date 2026-07-30", rate, date)
	}
}

// The BOJ releases table links each statement PDF beside its date; the
// newest Statement on Monetary Policy row is the one to read.
func TestBOJStatementRowIsTheNewestStatement(t *testing.T) {
	raw, err := os.ReadFile("testdata/rates/boj_listing.md")
	if err != nil {
		t.Fatal(err)
	}
	m := bojStatementRow(string(raw))
	if m == nil {
		t.Fatalf("no statement row matched")
	}
	if got := monthDayYearToISO(m[1], m[2], m[3]); got == nil || *got != "2026-07-31" {
		t.Fatalf("date = %v, want 2026-07-31", got)
	}
	if m[4] != "https://www.boj.or.jp/en/mopo/mpmdeci/mpr_2026/k260731a.pdf" {
		t.Fatalf("pdf = %s", m[4])
	}
}
