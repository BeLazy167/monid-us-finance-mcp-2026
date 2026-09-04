package service

import (
	"context"
	"strings"
	"testing"
)

// secFixture mirrors SEC's real company_tickers.json shape: an object keyed
// by row index, not an array, with unpadded integer CIKs. Row 2 repeats
// row 0's CIK, standing in for a company with two share classes.
const secFixture = `{
 "0": {"cik_str": 1045810, "ticker": "NVDA", "title": "NVIDIA CORP"},
 "1": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."},
 "2": {"cik_str": 1045810, "ticker": "NVDA.W", "title": "NVIDIA CORP"}
}`

// scrapeOutcome wraps a body the way context.dev's scraper does.
func scrapeOutcome(body string) map[string]fakeOutcome {
	return map[string]fakeOutcome{
		"context.dev /web/scrape/html": {output: map[string]any{"success": true, "html": body}},
	}
}

func newCIKCallCtx(t *testing.T, outcomes map[string]fakeOutcome) *callCtx {
	t.Helper()
	resetSECCatalogCache()
	t.Cleanup(resetSECCatalogCache)
	svc, _ := newTestService(t, outcomes)
	return svc.newCallCtx(context.Background(), "key", "test")
}

// TestListFilingsCIKs_MatchesSECFileOrderAndFormat pins the two properties
// that make this route byte-identical to Financial Datasets': SEC's own row
// order is preserved, and CIKs stay unpadded with share-class duplicates
// kept rather than deduplicated.
func TestListFilingsCIKs_MatchesSECFileOrderAndFormat(t *testing.T) {
	cc := newCIKCallCtx(t, scrapeOutcome(secFixture))
	result, err := cc.listFilingsCIKs(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	if body["resource"] != "filings" {
		t.Fatalf("resource = %v, want filings", body["resource"])
	}
	ciks, _ := body["ciks"].([]any)
	want := []any{"1045810", "320193", "1045810"}
	if len(ciks) != len(want) {
		t.Fatalf("got %d ciks, want %d: %#v", len(ciks), len(want), ciks)
	}
	for i := range want {
		if ciks[i] != want[i] {
			t.Fatalf("cik[%d] = %v, want %v (SEC file order preserved, duplicates kept)", i, ciks[i], want[i])
		}
	}
}

// TestListCompanyFactsCIKs_DedupesAndPads covers the other route's contract:
// same source, but deduplicated, zero-padded to 10 digits and sorted.
func TestListCompanyFactsCIKs_DedupesAndPads(t *testing.T) {
	cc := newCIKCallCtx(t, scrapeOutcome(secFixture))
	result, err := cc.listCompanyFactsCIKs(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body := jsonRoundTrip(t, result.Value)
	if body["resource"] != "company_facts" {
		t.Fatalf("resource = %v, want company_facts", body["resource"])
	}
	ciks, _ := body["ciks"].([]any)
	want := []any{"0000320193", "0001045810"}
	if len(ciks) != len(want) {
		t.Fatalf("got %d ciks, want %d (duplicate share class must collapse): %#v", len(ciks), len(want), ciks)
	}
	for i := range want {
		if ciks[i] != want[i] {
			t.Fatalf("cik[%d] = %v, want %v (padded to 10 digits, sorted)", i, ciks[i], want[i])
		}
	}
}

// TestSECCatalog_SchemaDriftOnNonContiguousKeys pins the one structural
// assumption this parser makes: SEC's index keys run 0..n-1, and recovering
// file order depends on that. A gap must fail loudly, never silently
// reorder or truncate the list.
func TestSECCatalog_SchemaDriftOnNonContiguousKeys(t *testing.T) {
	cc := newCIKCallCtx(t, scrapeOutcome(
		`{"0": {"cik_str": 1, "ticker": "A", "title": "A"}, "7": {"cik_str": 2, "ticker": "B", "title": "B"}}`))
	if _, err := cc.listFilingsCIKs(map[string]any{}); err == nil {
		t.Fatal("expected schema drift when SEC's index keys are not contiguous")
	}
}

// TestSECCatalog_EmptyScrapeIsAnError confirms an empty scrape surfaces as
// an error rather than an empty list, so a failed fetch can never read as
// "this server covers no CIKs".
func TestSECCatalog_EmptyScrapeIsAnError(t *testing.T) {
	cc := newCIKCallCtx(t, map[string]fakeOutcome{
		"context.dev /web/scrape/html": {output: map[string]any{"success": true, "html": ""}},
	})
	_, err := cc.listFilingsCIKs(map[string]any{})
	if err == nil {
		t.Fatal("expected an error when the scrape returns no content")
	}
	if !strings.Contains(err.Error(), "no content") {
		t.Fatalf("error should say the fetch returned nothing, got: %v", err)
	}
}
