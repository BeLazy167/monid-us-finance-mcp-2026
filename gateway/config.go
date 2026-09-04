// Package main implements the monid-finance edge gateway: a stdlib-only
// reverse proxy with API-key auth, rate limiting, response caching and a
// static site, in front of the Python Financial-Datasets-compatible API.
package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime settings for the gateway, read from env vars.
type Config struct {
	// Upstream is the base URL of the Python API service.
	Upstream *url.URL
	// APIKeys is the list of accepted X-API-KEY values. Empty means any
	// non-empty key passes (demo mode for keyed callers).
	APIKeys []string
	// CORSAllowedOrigins is the list of origins allowed to call the API.
	// Empty means any origin is allowed and the request Origin is reflected.
	CORSAllowedOrigins []string
	// DemoTickers enables keyless access: a GET without X-API-KEY whose
	// ?ticker= value is in this set is allowed through (rate limited under
	// the shared key "demo").
	DemoTickers map[string]bool
	// RateLimitPerMinute is the token-bucket capacity/refill rate per key.
	RateLimitPerMinute int
	// Port is the listen address port.
	Port string
	// WebsiteRoot is the directory served by the static site handler.
	WebsiteRoot string
	// UpstreamAPIKey is the X-API-KEY presented to the Python service. It is
	// injected for keyless demo traffic so the upstream still sees a key.
	UpstreamAPIKey string
}

// envOr returns the value of env key, or def when unset/blank.
func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// splitList splits a comma-separated env value into trimmed non-empty items.
func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// loadConfig reads configuration from environment variables.
func loadConfig() (Config, error) {
	var cfg Config

	upstream := envOr("UPSTREAM", "http://127.0.0.1:8000")
	u, err := url.Parse(upstream)
	if err != nil {
		return cfg, fmt.Errorf("invalid UPSTREAM %q: %w", upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return cfg, fmt.Errorf("UPSTREAM must include scheme and host, got %q", upstream)
	}
	cfg.Upstream = u

	cfg.APIKeys = splitList(os.Getenv("GATEWAY_API_KEYS"))
	cfg.CORSAllowedOrigins = splitList(os.Getenv("CORS_ALLOWED_ORIGINS"))

	cfg.DemoTickers = make(map[string]bool)
	for _, t := range splitList(envOr("DEMO_TICKERS", "AAPL,MSFT,NVDA")) {
		cfg.DemoTickers[strings.ToUpper(t)] = true
	}

	rate := 60
	if v := strings.TrimSpace(os.Getenv("RATE_LIMIT_PER_MINUTE")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cfg, fmt.Errorf("invalid RATE_LIMIT_PER_MINUTE %q", v)
		}
		rate = n
	}
	cfg.RateLimitPerMinute = rate

	cfg.Port = envOr("PORT", "8080")
	cfg.WebsiteRoot = envOr("WEBSITE_ROOT", "../website")
	cfg.UpstreamAPIKey = envOr("UPSTREAM_API_KEY", "gateway")
	return cfg, nil
}
