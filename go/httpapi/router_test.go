package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz_OpenNoAuth(t *testing.T) {
	rt := newTestRouter(newFakeCaller(), nil)
	rec := doGet(t, rt, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %#v, want {status: ok}", body)
	}
}

func TestHealthz_RejectsNonGet(t *testing.T) {
	rt := newTestRouter(newFakeCaller(), nil)
	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("POST /healthz should not succeed")
	}
}

// TestMCPMountedAtBothPaths proves the MCP handler answers at both /mcp and
// /api, with no REST auth gate in front of it (the dispatcher owns key
// handling — see cmd/server's Dispatcher adapter).
func TestMCPMountedAtBothPaths(t *testing.T) {
	var gotPaths []string
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})
	rt := newTestRouter(newFakeCaller(), func(cfg *Config) { cfg.MCPHandler = mcp })

	for _, path := range []string{"/mcp", "/api"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200: %s", path, rec.Code, rec.Body.String())
		}
	}
	if len(gotPaths) != 2 {
		t.Fatalf("mcp handler invoked %d times, want 2: %v", len(gotPaths), gotPaths)
	}
}

func TestMCPMount_RateLimited(t *testing.T) {
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	rt := newTestRouter(newFakeCaller(), func(cfg *Config) {
		cfg.MCPHandler = mcp
		cfg.RateLimitPerMinute = 1
	})
	headers := map[string]string{apiKeyHeader: testAPIKey}
	rec := doGet(t, rt, "/mcp", headers)
	if rec.Code != http.StatusOK {
		t.Fatalf("first call status = %d, want 200", rec.Code)
	}
	rec = doGet(t, rt, "/mcp", headers)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second call status = %d, want 429", rec.Code)
	}
}

func TestCORS_ReflectsOriginAndAnswersPreflight(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{}}}
	rt := newTestRouter(caller, nil)

	req := httptest.NewRequest(http.MethodOptions, "/company/facts", nil)
	req.Header.Set("Origin", "https://example.org")
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.org" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want reflected origin", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/company/facts?ticker=AAPL", nil)
	req.Header.Set("Origin", "https://example.org")
	req.Header.Set(apiKeyHeader, testAPIKey)
	rec = httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.org" {
		t.Fatalf("GET Access-Control-Allow-Origin = %q, want reflected origin", got)
	}
}

func TestCORS_RestrictsToAllowedOrigins(t *testing.T) {
	caller := newFakeCaller()
	caller.results["get_company_facts"] = Result{Value: map[string]any{"company_facts": map[string]any{}}}
	rt := newTestRouter(caller, func(cfg *Config) { cfg.CORSAllowedOrigins = []string{"https://allowed.example"} })

	req := httptest.NewRequest(http.MethodGet, "/company/facts?ticker=AAPL", nil)
	req.Header.Set("Origin", "https://blocked.example")
	req.Header.Set(apiKeyHeader, testAPIKey)
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want unset for a disallowed origin", got)
	}
	// The request itself still succeeds; CORS only gates browser JS access.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
