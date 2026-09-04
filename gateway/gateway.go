package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"time"
)

// gateway routes requests: /healthz, then the proxied API prefixes, then the
// static site. It owns auth, rate limiting, CORS and the response cache.
type gateway struct {
	cfg       Config
	proxy     *httputil.ReverseProxy
	validKeys map[string]bool
	limiter   *rateLimiter
	cache     *responseCache
}

// newGateway builds the gateway from a resolved configuration.
func newGateway(cfg Config) *gateway {
	valid := make(map[string]bool, len(cfg.APIKeys))
	for _, k := range cfg.APIKeys {
		valid[k] = true
	}
	return &gateway{
		cfg:       cfg,
		proxy:     newProxy(cfg.Upstream),
		validKeys: valid,
		limiter:   newRateLimiter(),
		cache:     newResponseCache(),
	}
}

// ServeHTTP applies the route precedence: health, then API (proxied), then
// static. The proxied API always wins over same-named docs pages.
func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/healthz":
		g.handleHealth(w, r)
	case isProxyPath(r.URL.Path):
		g.handleProxy(w, r)
	default:
		g.handleStatic(w, r)
	}
}

// handleHealth serves the liveness probe without auth.
func (g *gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleProxy runs the API pipeline: CORS (with OPTIONS preflight), auth,
// rate limit, then cache-or-proxy. Only explicitly allowed origins get CORS
// headers; with no CORS_ALLOWED_ORIGINS configured any origin is reflected.
func (g *gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	if origin, ok := g.corsOrigin(r); ok {
		addCORSHeaders(w, origin)
		if r.Method == http.MethodOptions {
			// Preflight: answer without touching auth or the upstream.
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}

	key, allowed, hasAPIKey := g.authorize(w, r)
	if !allowed {
		return
	}

	if !g.limiter.allow(key, g.cfg.RateLimitPerMinute) {
		writeJSONError(w, http.StatusTooManyRequests, "rate_limited", "Rate limit exceeded. Retry shortly.")
		return
	}

	// The Python service always requires a key; present the gateway's own
	// upstream key so keyless demo traffic still authenticates there.
	r.Header.Set(apiKeyHeader, g.cfg.UpstreamAPIKey)

	// Demo traffic is the most repeated traffic, so it is cached too.
	cacheable := r.Method == http.MethodGet && !isMCPOrAPI(r.URL.Path)
	_ = hasAPIKey
	if cacheable {
		if entry, hit := g.cache.get(cacheKey(r), time.Now()); hit {
			w.Header().Set("X-Cache", "hit")
			w.Header().Set("Content-Type", entry.contentType)
			w.WriteHeader(entry.status)
			_, _ = w.Write(entry.body)
			return
		}
		w.Header().Set("X-Cache", "miss")
	}

	tw := &teeWriter{ResponseWriter: w}
	g.proxy.ServeHTTP(tw, r)

	if cacheable && tw.status > 0 && tw.status < http.StatusBadRequest {
		g.cache.put(cacheKey(r), cacheEntry{
			body:        tw.buf.Bytes(),
			contentType: w.Header().Get("Content-Type"),
			status:      tw.status,
			expiresAt:   time.Now().Add(ttlForPath(r.URL.Path)),
		})
	}
}

// corsOrigin returns the origin to reflect in CORS headers, if any.
func (g *gateway) corsOrigin(r *http.Request) (string, bool) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return "", false
	}
	if len(g.cfg.CORSAllowedOrigins) == 0 {
		return origin, true // env unset: reflect any origin
	}
	for _, allowed := range g.cfg.CORSAllowedOrigins {
		if allowed == origin {
			return origin, true
		}
	}
	return "", false
}

// addCORSHeaders sets the reflect-origin CORS headers on the response.
func addCORSHeaders(w http.ResponseWriter, origin string) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Headers", "Content-Type, X-API-KEY, Authorization")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
}

// teeWriter streams the response to the client while buffering a copy for the
// response cache. Headers pass straight through so the client sees upstream
// headers (including hop-by-hop stripping done by the ReverseProxy) live.
type teeWriter struct {
	http.ResponseWriter
	buf    bytes.Buffer
	status int
}

func (w *teeWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *teeWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}
