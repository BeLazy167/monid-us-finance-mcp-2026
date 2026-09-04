package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

const apiKeyHeader = "X-API-KEY"

// authorize validates the request against the API-key policy and returns:
//   - key: the effective rate-limit key (the provided API key, or "demo"
//     for keyless demo-mode requests)
//   - allowed: whether the request may proceed
//   - hasAPIKey: true when a valid X-API-KEY header was presented. Only
//     requests with a real key may be cached.
//
// Policy:
//   - A valid X-API-KEY header is always accepted. When no GATEWAY_API_KEYS
//     are configured, any non-empty key passes (demo mode for keyed callers).
//   - Keyless requests are allowed only for GETs whose ?ticker= value is in
//     DEMO_TICKERS (Financial-Datasets-style instant tryout); these are
//     rate limited under the shared key "demo".
//   - Missing or invalid keys receive a 401 with an FD-shaped error body.
func (g *gateway) authorize(w http.ResponseWriter, r *http.Request) (key string, allowed bool, hasAPIKey bool) {
	if provided := r.Header.Get(apiKeyHeader); provided != "" {
		if len(g.cfg.APIKeys) == 0 || g.validKeys[provided] {
			return provided, true, true
		}
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid X-API-KEY header.")
		return "", false, false
	}

	// Keyless demo mode: only allow GETs for configured demo tickers.
	if r.Method == http.MethodGet {
		ticker := strings.ToUpper(r.URL.Query().Get("ticker"))
		if ticker != "" && g.cfg.DemoTickers[ticker] {
			return demoKey, true, false
		}
	}

	writeJSONError(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid X-API-KEY header.")
	return "", false, false
}

// demoKey is the shared rate-limit identity for keyless demo requests.
const demoKey = "demo"

// writeJSONError writes an FD-shaped JSON error response
// ({"error": code, "message": text}) with the given status.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
