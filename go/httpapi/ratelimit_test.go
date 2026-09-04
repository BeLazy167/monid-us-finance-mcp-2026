package httpapi

import (
	"net/http"
	"testing"
	"time"
)

func TestBucket_AllowRefillsOverTime(t *testing.T) {
	b := &bucket{tokens: 2, last: time.Now()}
	if !b.allow(2) {
		t.Fatalf("first call should be allowed")
	}
	if !b.allow(2) {
		t.Fatalf("second call should be allowed (capacity 2)")
	}
	if b.allow(2) {
		t.Fatalf("third immediate call should be rejected (bucket exhausted)")
	}
}

func TestRateLimiter_PerKeyBuckets(t *testing.T) {
	rl := newRateLimiter()
	if !rl.allow("a", 1) {
		t.Fatalf("key a first call should be allowed")
	}
	if rl.allow("a", 1) {
		t.Fatalf("key a second immediate call should be rejected")
	}
	if !rl.allow("b", 1) {
		t.Fatalf("key b has its own bucket and should be allowed")
	}
}

// TestRouter_RateLimit429PastTheLimit proves the router enforces
// RATE_LIMIT_PER_MINUTE end to end: calls past the configured capacity get
// 429 with the FD error shape.
func TestRouter_RateLimit429PastTheLimit(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{}}}
	rt := newTestRouter(caller, func(cfg *Config) { cfg.RateLimitPerMinute = 2 })
	headers := map[string]string{apiKeyHeader: testAPIKey}

	for i := 0; i < 2; i++ {
		rec := doGet(t, rt, "/company/facts?ticker=AAPL", headers)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d, want 200: %s", i, rec.Code, rec.Body.String())
		}
	}

	rec := doGet(t, rt, "/company/facts?ticker=AAPL", headers)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["error"] != "rate_limited" {
		t.Fatalf("error = %v, want rate_limited", body["error"])
	}
	if _, ok := body["message"].(string); !ok {
		t.Fatalf("message missing/not a string: %#v", body)
	}
}

// TestRouter_RateLimitIsPerCallerKey proves one caller's exhausted bucket
// does not block a different caller.
func TestRouter_RateLimitIsPerCallerKey(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{}}}
	rt := newTestRouter(caller, func(cfg *Config) { cfg.RateLimitPerMinute = 1 })

	rec := doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: "key-a"})
	if rec.Code != http.StatusOK {
		t.Fatalf("key-a first call status = %d, want 200", rec.Code)
	}
	rec = doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: "key-a"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("key-a second call status = %d, want 429", rec.Code)
	}
	rec = doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: "key-b"})
	if rec.Code != http.StatusOK {
		t.Fatalf("key-b call status = %d, want 200 (separate bucket)", rec.Code)
	}
}
