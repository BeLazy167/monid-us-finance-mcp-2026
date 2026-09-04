package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newGetRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodGet, target, nil)
}

func TestResponseCache_PutGetExpiry(t *testing.T) {
	c := newResponseCache()
	now := time.Now()
	c.put("k", cacheEntry{body: []byte("hello"), status: 200, expiresAt: now.Add(time.Minute)})

	entry, ok := c.get("k", now)
	if !ok || string(entry.body) != "hello" {
		t.Fatalf("get before expiry = %v, %#v", ok, entry)
	}

	_, ok = c.get("k", now.Add(2*time.Minute))
	if ok {
		t.Fatalf("get after expiry should miss")
	}
}

func TestResponseCache_EvictsOldestBeyondCapacity(t *testing.T) {
	c := newResponseCache()
	now := time.Now()
	for i := 0; i < maxCacheEntries+10; i++ {
		c.put(string(rune(i)), cacheEntry{status: 200, expiresAt: now.Add(time.Minute)})
	}
	if c.order.Len() != maxCacheEntries {
		t.Fatalf("cache holds %d entries, want %d", c.order.Len(), maxCacheEntries)
	}
}

func TestCacheKey_OrderIndependentAndKeyed(t *testing.T) {
	r1 := newGetRequest(t, "/prices?ticker=AAPL&start_date=2025-01-01")
	r2 := newGetRequest(t, "/prices?start_date=2025-01-01&ticker=AAPL")
	if cacheKey(r1, "k") != cacheKey(r2, "k") {
		t.Fatalf("cache keys differ for reordered query params")
	}
	if cacheKey(r1, "k1") == cacheKey(r1, "k2") {
		t.Fatalf("cache keys must differ across caller identities")
	}
}

// TestRouter_XCacheHitAndMiss ports the gateway's cache behavior through
// the router: first GET misses and is cached, second identical GET hits.
func TestRouter_XCacheHitAndMiss(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{"ticker": "AAPL"}}}
	rt := newTestRouter(caller, nil)
	headers := map[string]string{apiKeyHeader: testAPIKey}

	rec1 := doGet(t, rt, "/company/facts?ticker=AAPL", headers)
	if rec1.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec1.Code, rec1.Body.String())
	}
	if got := rec1.Header().Get("X-Cache"); got != "miss" {
		t.Fatalf("X-Cache = %q, want miss", got)
	}

	rec2 := doGet(t, rt, "/company/facts?ticker=AAPL", headers)
	if got := rec2.Header().Get("X-Cache"); got != "hit" {
		t.Fatalf("X-Cache = %q, want hit", got)
	}
	if rec2.Body.String() != rec1.Body.String() {
		t.Fatalf("cached body differs from the original response")
	}

	if len(caller.calls) != 1 {
		t.Fatalf("caller was invoked %d times, want 1 (second GET must be served from cache)", len(caller.calls))
	}
}

// TestRouter_MCPNeverCached proves /mcp and /api never see the cache layer:
// they carry no X-Cache header at all.
func TestRouter_MCPNeverCached(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	for _, path := range []string{"/mcp", "/api"} {
		rec := doGet(t, rt, path, nil)
		if got := rec.Header().Get("X-Cache"); got != "" {
			t.Fatalf("%s: X-Cache = %q, want unset (never cached)", path, got)
		}
	}
}
