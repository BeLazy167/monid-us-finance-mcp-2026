package httpapi

import (
	"net/http"
	"testing"
)

func TestAuth_MissingKey401(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/company/facts?ticker=AAPL", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["error"] != "unauthorized" {
		t.Fatalf("error = %v, want unauthorized", body["error"])
	}
	if _, ok := body["message"].(string); !ok {
		t.Fatalf("message missing/not a string: %#v", body)
	}
	if len(caller.calls) != 0 {
		t.Fatalf("an unauthorized request must never reach the caller")
	}
}

func TestAuth_EmptyKeyHeader401(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: ""})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuth_MalformedKey401(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil)
	// A key carrying a control character (header-injection shaped) is
	// rejected outright, never forwarded to the caller.
	rec := doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: "abc\x00def"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuth_AnyNonEmptyKeyAcceptedWhenAllowlistUnset(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{"ticker": "AAPL"}}}
	rt := newTestRouter(caller, nil)
	rec := doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: "anything-at-all"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := caller.lastCall().apiKey; got != "anything-at-all" {
		t.Fatalf("caller saw apiKey = %q, want the caller's own key forwarded verbatim", got)
	}
}

func TestAuth_AllowlistRestrictsKeys(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{}}}
	rt := newTestRouter(caller, func(cfg *Config) { cfg.AllowedAPIKeys = []string{"good-key"} })

	rec := doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: "bad-key"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-key status = %d, want 401", rec.Code)
	}

	rec = doGet(t, rt, "/company/facts?ticker=AAPL", map[string]string{apiKeyHeader: "good-key"})
	if rec.Code != http.StatusOK {
		t.Fatalf("good-key status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestAuth_DemoModeKeylessDemoTicker(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_stock_price"] = Result{Value: map[string]any{"snapshot": map[string]any{"ticker": "AAPL"}}}
	rt := newTestRouter(caller, func(cfg *Config) { cfg.DemoMonidAPIKey = "demo-monid-key" })

	rec := doGet(t, rt, "/prices/snapshot?ticker=AAPL", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := caller.lastCall().apiKey; got != "demo-monid-key" {
		t.Fatalf("caller saw apiKey = %q, want the operator's demo key", got)
	}

	// A non-demo ticker is still unauthorized without a key.
	rec = doGet(t, rt, "/prices/snapshot?ticker=TSLA", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-demo ticker status = %d, want 401", rec.Code)
	}
}

func TestAuth_DemoModeDisabledByDefault(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, nil) // no DemoMonidAPIKey configured
	rec := doGet(t, rt, "/prices/snapshot?ticker=AAPL", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 when DEMO_MONID_API_KEY is unset", rec.Code)
	}
}

func TestAuth_DemoModeOnlyAppliesToGET(t *testing.T) {
	caller := newFakeCaller()
	rt := newTestRouter(caller, func(cfg *Config) { cfg.DemoMonidAPIKey = "demo-monid-key" })
	rec := doPost(t, rt, "/financials/search/screener", map[string]any{"filters": []map[string]any{}}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (POST never qualifies for keyless demo)", rec.Code)
	}
}

func TestIsWellFormedAPIKey(t *testing.T) {
	cases := map[string]bool{
		"sk-live-abc123": true,
		"":               true, // emptiness is handled by the caller, not this check
		"abc\ndef":       false,
		"abc\rdef":       false,
		"abc\x7fdef":     false,
	}
	for key, want := range cases {
		if got := isWellFormedAPIKey(key); got != want {
			t.Fatalf("isWellFormedAPIKey(%q) = %v, want %v", key, got, want)
		}
	}
}
