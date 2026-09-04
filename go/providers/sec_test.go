package providers

import "testing"

func TestDeriveAccession(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "no dashes in path",
			url:  "https://www.sec.gov/Archives/edgar/data/320193/000032019326000001/aapl-20251231.htm",
			want: "0000320193-26-000001",
		},
		{
			name: "already dashed",
			url:  "https://www.sec.gov/Archives/edgar/data/320193/0000320193-25-000079/aapl-20241231.htm",
			want: "0000320193-25-000079",
		},
		{
			name: "10-K fixture accession",
			url:  "https://www.sec.gov/Archives/edgar/data/320193/000032019325000079/aapl-20250930.htm",
			want: "0000320193-25-000079",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveAccession(tc.url)
			if got == nil {
				t.Fatalf("DeriveAccession(%q) = nil, want %q", tc.url, tc.want)
			}
			if *got != tc.want {
				t.Errorf("DeriveAccession(%q) = %q, want %q", tc.url, *got, tc.want)
			}
		})
	}
}

func TestDeriveAccession_NoMatch(t *testing.T) {
	cases := []string{
		"https://www.sec.gov/Archives/edgar/data/320193/aapl-20251231.htm",
		// A 19-digit run cannot contain an 18-digit accession: the Python
		// regex's negative lookaround rejects every start position.
		"https://www.sec.gov/Archives/edgar/data/1/0000320193260000012x.htm",
		"not a url at all: %zz",
	}
	for _, url := range cases {
		if got := DeriveAccession(url); got != nil {
			t.Errorf("DeriveAccession(%q) = %q, want nil", url, *got)
		}
	}
}
