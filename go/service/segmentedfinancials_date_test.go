package service

import "testing"

// The extractor copies the filing's own date wording often enough to
// matter: Apple's 10-K prints "September 27, 2025", and reading only ISO
// dates dropped every row and failed the whole route.
func TestParseSegmentPeriodEnd(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2025-09-27", "2025-09-27"},
		{"September 27, 2025", "2025-09-27"},
		{"Sept. 27, 2025", "2025-09-27"},
		{"27 September 2025", "2025-09-27"},
		{"2025-09-27T00:00:00Z", "2025-09-27"},
	} {
		got := parseSegmentPeriodEnd(tc.in)
		if got == nil || got.Format(dateLayout) != tc.want {
			t.Fatalf("parseSegmentPeriodEnd(%q) = %v, want %s", tc.in, got, tc.want)
		}
	}
	if got := parseSegmentPeriodEnd("fiscal 2025"); got != nil {
		t.Fatalf("a text with no date must yield nil, got %v", got)
	}
}
