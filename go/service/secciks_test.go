package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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

// newSECStub points secCompanyTickersURL at a local server and restores it
// (and the shared cache) when the test finishes.
func newSECStub(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	captured := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case captured <- r.Header.Get("User-Agent"):
		default:
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := secCompanyTickersURL
	secCompanyTickersURL = srv.URL
	resetSECCatalogCache()
	t.Cleanup(func() {
		secCompanyTickersURL = prev
		resetSECCatalogCache()
		srv.Close()
	})
	return srv
}

// newSECTestService builds a Service with a REAL http.Client. The shared
// newTestService helper installs a fake transport that intercepts every
// request by Monid provider/endpoint, so it cannot reach the httptest
// server standing in for sec.gov here.
func newSECTestService() *Service {
	return New(Config{HTTP: &http.Client{}, Allowlist: allowAll{}, MaxConcurrentRuns: 8})
}

func withSECUserAgent(t *testing.T, value string) {
	t.Helper()
	prev, had := os.LookupEnv("SEC_USER_AGENT")
	if value == "" {
		_ = os.Unsetenv("SEC_USER_AGENT")
	} else {
		t.Setenv("SEC_USER_AGENT", value)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("SEC_USER_AGENT", prev)
		} else if value == "" {
			_ = os.Unsetenv("SEC_USER_AGENT")
		}
	})
}

// TestListFilingsCIKs_MatchesSECFileOrderAndFormat pins the two properties
// that make this route byte-identical to Financial Datasets': SEC's own row
// order is preserved, and CIKs stay unpadded with share-class duplicates
// kept rather than deduplicated.
func TestListFilingsCIKs_MatchesSECFileOrderAndFormat(t *testing.T) {
	newSECStub(t, http.StatusOK, secFixture)
	withSECUserAgent(t, "test-agent test@example.com")

	svc := newSECTestService()
	cc := svc.newCallCtx(context.Background(), "key", "list_filings_ciks")
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
			t.Fatalf("cik[%d] = %v, want %v (SEC file order must be preserved, duplicates kept)", i, ciks[i], want[i])
		}
	}
}

// TestListCompanyFactsCIKs_DedupesAndPads covers the other route's contract:
// same source, but deduplicated, zero-padded to 10 digits and sorted.
func TestListCompanyFactsCIKs_DedupesAndPads(t *testing.T) {
	newSECStub(t, http.StatusOK, secFixture)
	withSECUserAgent(t, "test-agent test@example.com")

	svc := newSECTestService()
	cc := svc.newCallCtx(context.Background(), "key", "list_company_facts_ciks")
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
			t.Fatalf("cik[%d] = %v, want %v (padded to 10 digits, sorted ascending)", i, ciks[i], want[i])
		}
	}
}

// TestSECUserAgent_RequiredWithActionableMessage guards the deliberate
// absence of a default: SEC 403s a User-Agent with no contact email, and
// shipping a placeholder address would misattribute every deployment's
// traffic, so an unset value must fail by naming the variable to set.
func TestSECUserAgent_RequiredWithActionableMessage(t *testing.T) {
	newSECStub(t, http.StatusOK, secFixture)
	withSECUserAgent(t, "")

	svc := newSECTestService()
	cc := svc.newCallCtx(context.Background(), "key", "list_filings_ciks")
	_, err := cc.listFilingsCIKs(map[string]any{})
	if err == nil {
		t.Fatal("expected an error when SEC_USER_AGENT is unset")
	}
	if !strings.Contains(err.Error(), "SEC_USER_AGENT") {
		t.Fatalf("error must name the variable to set, got: %v", err)
	}
}

// TestSECCatalog_SchemaDriftOnNonContiguousKeys pins the one structural
// assumption this parser makes: SEC's index keys run 0..n-1, and recovering
// file order depends on that. A gap must fail loudly, never silently
// reorder or truncate the list.
func TestSECCatalog_SchemaDriftOnNonContiguousKeys(t *testing.T) {
	newSECStub(t, http.StatusOK, `{"0": {"cik_str": 1, "ticker": "A", "title": "A"}, "7": {"cik_str": 2, "ticker": "B", "title": "B"}}`)
	withSECUserAgent(t, "test-agent test@example.com")

	svc := newSECTestService()
	cc := svc.newCallCtx(context.Background(), "key", "list_filings_ciks")
	if _, err := cc.listFilingsCIKs(map[string]any{}); err == nil {
		t.Fatal("expected schema drift when SEC's index keys are not contiguous")
	}
}

// TestSECCatalog_UpstreamStatusSurfaces confirms a non-200 from sec.gov
// becomes an error naming the status rather than an empty list, so a 403
// can never read as "this server covers no CIKs".
func TestSECCatalog_UpstreamStatusSurfaces(t *testing.T) {
	newSECStub(t, http.StatusForbidden, "denied")
	withSECUserAgent(t, "test-agent test@example.com")

	svc := newSECTestService()
	cc := svc.newCallCtx(context.Background(), "key", "list_filings_ciks")
	_, err := cc.listFilingsCIKs(map[string]any{})
	if err == nil {
		t.Fatal("expected an error for a non-200 SEC response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error should name the upstream status, got: %v", err)
	}
}
