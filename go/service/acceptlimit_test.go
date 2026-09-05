package service

import "testing"

// limit is a maximum a caller may ask for, not an assertion about what the
// upstream feed holds. Rejecting a request for more rows than exist turned
// a working ai-hedge-fund call into a 400 against this server while the
// same call succeeded against Financial Datasets.
func TestAcceptLimit(t *testing.T) {
	for _, tc := range []struct {
		name              string
		asked, max, avail int
		want              int
		wantErr           bool
	}{
		{name: "more than the feed holds is capped, not refused", asked: 1000, max: 5000, avail: 15, want: 15},
		{name: "within the feed is honoured", asked: 5, max: 5000, avail: 15, want: 5},
		{name: "exactly the feed size", asked: 15, max: 5000, avail: 15, want: 15},
		{name: "past the contract ceiling is still refused", asked: 6000, max: 5000, avail: 15, wantErr: true},
		{name: "zero is refused", asked: 0, max: 100, avail: 10, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := acceptLimit(tc.asked, tc.max, tc.avail)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("asked %d: expected an error, got %d", tc.asked, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("asked %d: unexpected error %v", tc.asked, err)
			}
			if got != tc.want {
				t.Fatalf("asked %d: got %d, want %d", tc.asked, got, tc.want)
			}
		})
	}
}
