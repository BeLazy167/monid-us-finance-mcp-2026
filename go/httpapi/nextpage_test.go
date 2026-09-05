package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The cursor carries an offset only, so the continuation link must carry
// the caller's own query. A link of "?cursor=..." alone sent clients back
// without their ticker, and the next request failed as bad_request.
func TestNextPageURLKeepsTheQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/insider-trades?ticker=AAPL&limit=1000", nil)
	got, err := url.Parse(nextPageURL(r, "CURSOR"))
	if err != nil {
		t.Fatalf("not a URL: %v", err)
	}
	q := got.Query()
	if q.Get("ticker") != "AAPL" {
		t.Fatalf("ticker lost from the continuation link: %s", got)
	}
	if q.Get("limit") != "1000" {
		t.Fatalf("limit lost from the continuation link: %s", got)
	}
	if q.Get("cursor") != "CURSOR" {
		t.Fatalf("cursor = %q, want CURSOR", q.Get("cursor"))
	}
	if got.Path != "/insider-trades" {
		t.Fatalf("path = %q", got.Path)
	}
}
