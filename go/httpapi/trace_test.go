package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func tracedResult() Result {
	cost := 0.0006
	return Result{
		Value:      []map[string]any{{"ticker": "AAPL"}},
		WrapperKey: "income_statements",
		Paginate:   true,
		Trace: []TraceStep{
			{Provider: "defillama", Endpoint: "/equities/v1/statements", RunID: "01RUN", Status: "COMPLETED", CostUSD: &cost, Milliseconds: 412},
			{Provider: "defillama", Endpoint: "/equities/v1/filings", RunID: "01RUN2", Status: "COMPLETED", Milliseconds: 88, Cached: true},
		},
	}
}

// Provenance must never change a Financial Datasets response body, and must
// stay off the wire entirely until a caller asks for it.
func TestTraceHeaders_OptInOnly(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_income_statement"] = tracedResult()
	rt := newTestRouter(caller, nil)

	plain := doGet(t, rt, "/financials/income-statements?ticker=AAPL", map[string]string{apiKeyHeader: testAPIKey})
	if got := plain.Header().Get("X-Monid-Trace"); got != "" {
		t.Fatalf("trace must be absent unless requested, got %q", got)
	}
	body := decodeBody(t, plain)
	if _, leaked := body["trace"]; leaked {
		t.Fatalf("provenance must never enter the response body: %#v", body)
	}

	traced := doGet(t, rt, "/financials/income-statements?ticker=AAPL",
		map[string]string{apiKeyHeader: testAPIKey, "X-Monid-Trace": "1"})
	raw := traced.Header().Get("X-Monid-Trace")
	if raw == "" {
		t.Fatalf("trace header missing when requested")
	}
	var steps []TraceStep
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		t.Fatalf("trace header is not JSON: %v", err)
	}
	if len(steps) != 2 || steps[0].Provider != "defillama" || !steps[1].Cached {
		t.Fatalf("unexpected trace: %#v", steps)
	}
	if cost := traced.Header().Get("X-Monid-Cost-USD"); cost != "0.0006" {
		t.Fatalf("cost header = %q, want 0.0006 (a cache hit adds nothing)", cost)
	}
	if !bytes.Equal(plain.Body.Bytes(), traced.Body.Bytes()) {
		t.Fatalf("asking for provenance changed the body")
	}
}

// roundTripFunc lets the proxy test observe the upstream request without
// making one.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The proxy exists because Financial Datasets sends browsers no
// Access-Control-Allow-Origin. It must forward the caller's own key, to
// their host only, and never accept a Monid key in its place.
func TestFDProxy_ForwardsTheCallersOwnKey(t *testing.T) {
	var seen *http.Request
	rt := &restAPI{caller: newFakeCaller(), compareHTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(`{"income_statements":[]}`))), Header: http.Header{}}, nil
	})}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, comparePrefix+"financials/income-statements?ticker=AAPL", nil)
	req.Header.Set(fdKeyHeader, "fd-key-123")
	rt.fdProxy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if seen == nil {
		t.Fatalf("no upstream request was made")
	}
	if want := fdAPIBase + "/financials/income-statements?ticker=AAPL"; seen.URL.String() != want {
		t.Fatalf("target = %s, want %s", seen.URL, want)
	}
	if seen.Header.Get("X-API-KEY") != "fd-key-123" {
		t.Fatalf("the caller's Financial Datasets key was not forwarded")
	}
	if rec.Header().Get("X-FD-Elapsed-MS") == "" {
		t.Fatalf("the proxy must report how long Financial Datasets took")
	}
}

func TestFDProxy_RejectsMissingKeyAndNonGET(t *testing.T) {
	rt := &restAPI{caller: newFakeCaller()}

	rec := httptest.NewRecorder()
	rt.fdProxy(rec, httptest.NewRequest(http.MethodGet, comparePrefix+"financials/income-statements", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	post := httptest.NewRequest(http.MethodPost, comparePrefix+"financials/income-statements", nil)
	post.Header.Set(fdKeyHeader, "fd-key-123")
	rt.fdProxy(rec, post)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status = %d, want 405", rec.Code)
	}
}

func TestFormatUSD(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{{0, "0"}, {0.0006, "0.0006"}, {0.01, "0.01"}, {1.5, "1.5"}, {0.000001, "0.000001"}} {
		if got := formatUSD(tc.in); got != tc.want {
			t.Fatalf("formatUSD(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
