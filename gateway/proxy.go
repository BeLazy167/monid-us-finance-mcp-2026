package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// proxyPrefixes are the API path prefixes forwarded to the upstream. A prefix
// ending in "/" matches only paths under it; a prefix without a trailing slash
// matches the bare prefix and anything under it. Entrance exactly as given, so
// e.g. "/mcp" proxies /mcp and /mcp/... but leaves the static /mcp-tools docs
// page alone.
var proxyPrefixes = []string{
	"/financials/", "/financial-metrics/", "/earnings", "/filings", "/prices",
	"/news", "/insider-trades", "/macro/", "/kpi/", "/index-funds",
	"/institutional-holdings", "/company/", "/mcp", "/api", "/screener",
}

// isProxyPath reports whether p belongs to one of the API prefixes. It is the
// precedence gate that keeps the live API from being shadowed by static docs.
func isProxyPath(p string) bool {
	for _, prefix := range proxyPrefixes {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(p, prefix) {
				return true
			}
			continue
		}
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// isMCPOrAPI reports whether p is the proxied MCP JSON-RPC endpoint or the
// /api proxy. These are stateful and never cached.
func isMCPOrAPI(p string) bool {
	return p == "/mcp" || strings.HasPrefix(p, "/mcp/") ||
		p == "/api" || strings.HasPrefix(p, "/api/")
}

// newProxy returns a ReverseProxy that forwards to target. The Director only
// rewrites scheme/host, preserving the original path, query, method, body and
// all headers (including X-API-KEY).
func newProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeJSONError(w, http.StatusBadGateway, "upstream_error", "Upstream unavailable.")
		},
	}
}
