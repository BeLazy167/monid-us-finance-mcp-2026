package providers

import "net/url"

// DeriveAccession extracts an SEC accession number
// ("nnnnnnnnnn-nn-nnnnnn") from a filing URL. Ported from
// monid_finance_mcp.providers.us.filing_items.derive_accession, whose
// regex is r"(?<!\d)(\d{10})-?(\d{2})-?(\d{6})(?!\d)" applied to the URL
// path. Go's RE2 engine has no lookaround, so the same "18 digits, not
// part of a longer digit run, optional single dashes after digit 10 and
// digit 12" search is hand-rolled here to match identical semantics.
func DeriveAccession(rawURL string) *string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return findAccession(parsed.Path)
}

func findAccession(path string) *string {
	n := len(path)
	for i := 0; i < n; i++ {
		if !isASCIIDigit(path[i]) {
			continue
		}
		if i > 0 && isASCIIDigit(path[i-1]) {
			continue
		}
		pos := i
		group1, ok := takeDigits(path, &pos, 10)
		if !ok {
			continue
		}
		skipDash(path, &pos)
		group2, ok := takeDigits(path, &pos, 2)
		if !ok {
			continue
		}
		skipDash(path, &pos)
		group3, ok := takeDigits(path, &pos, 6)
		if !ok {
			continue
		}
		if pos < n && isASCIIDigit(path[pos]) {
			continue
		}
		accession := group1 + "-" + group2 + "-" + group3
		return &accession
	}
	return nil
}

func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func takeDigits(s string, pos *int, count int) (string, bool) {
	if *pos+count > len(s) {
		return "", false
	}
	for k := *pos; k < *pos+count; k++ {
		if !isASCIIDigit(s[k]) {
			return "", false
		}
	}
	out := s[*pos : *pos+count]
	*pos += count
	return out, true
}

func skipDash(s string, pos *int) {
	if *pos < len(s) && s[*pos] == '-' {
		*pos++
	}
}
