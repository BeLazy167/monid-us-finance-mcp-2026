package service

import "testing"

// A post-earnings-drift model reads eps_surprise to decide direction, so
// the verdict has to follow the reported figure against consensus.
func TestSurpriseVerdictAndPercent(t *testing.T) {
	for _, tc := range []struct {
		reported, consensus float64
		want                string
	}{
		{1.85, 1.73, "BEAT"},
		{1.60, 1.73, "MISS"},
		{1.73, 1.73, "MEET"},
	} {
		if got := surpriseVerdict(tc.reported, tc.consensus); got != tc.want {
			t.Fatalf("%v against %v = %s, want %s", tc.reported, tc.consensus, got, tc.want)
		}
	}
	if got := roundTo((1.85-1.73)/1.73*100, 2); got != 6.94 {
		t.Fatalf("surprise percent = %v, want 6.94", got)
	}
}

// Nasdaq labels a quarter by the month it ended in, and a report period
// is a full date, so the two only meet once the date is reduced.
func TestQuarterKey(t *testing.T) {
	for in, want := range map[string]string{
		"2025-09-27": "sep 2025",
		"2026-06-27": "jun 2026",
		"not-a-date": "",
	} {
		if got := quarterKey(in); got != want {
			t.Fatalf("quarterKey(%q) = %q, want %q", in, got, want)
		}
	}
}
