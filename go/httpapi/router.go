package httpapi

import (
	"context"
	"net/http"
	"strings"
)

// Result is one Caller.Call outcome in Financial Datasets shape. It mirrors
// go/service.Result field-for-field (see the shared contract); this package
// defines its own copy so it builds and tests independently of go/service,
// per the task's fallback instruction. cmd/server adapts the real
// go/service.Result to this type at the call site.
type Result struct {
	// Value is the bare FD value: a slice for list tools, an object for
	// snapshot/unwrapped tools. The MCP transport returns it as-is.
	Value any
	// WrapperKey is the REST envelope key ("income_statements", "prices",
	// ...). Empty means the REST response body IS Value, unwrapped.
	WrapperKey string
	// Paginate is true when REST should apply cursor pagination to Value
	// (which must then be a slice). MCP never paginates.
	Paginate bool
}

// Caller runs one FD tool with the caller's own Monid API key. Every Monid
// call it makes bills that caller's wallet. apiKey is never logged.
//
// This is the narrow interface go/httpapi depends on instead of importing
// go/service directly, so this package (and its tests) build without
// go/service existing yet. cmd/server/main.go adapts the real
// go/service.Service to this interface.
type Caller interface {
	Call(ctx context.Context, apiKey, tool string, args map[string]any) (Result, error)
}

// Config wires one Router.
type Config struct {
	// Caller runs FD tools against the caller's own Monid key.
	Caller Caller
	// MCPHandler answers MCP JSON-RPC requests (mcpserver.New(dispatcher)).
	// Mounted verbatim at both /mcp and /api.
	MCPHandler http.Handler
	// StaticDir is the website root served for any path that is not a
	// REST/MCP/health route.
	StaticDir string
	// AllowedAPIKeys optionally restricts caller-supplied keys (see
	// AuthConfig.AllowedKeys). Empty means any well-formed key passes.
	AllowedAPIKeys []string
	// DemoMonidAPIKey optionally enables keyless demo tryout (see
	// AuthConfig.DemoMonidAPIKey).
	DemoMonidAPIKey string
	// RateLimitPerMinute is the token-bucket capacity/refill rate per
	// caller key. Defaults to 60 when <= 0.
	RateLimitPerMinute int
	// CORSAllowedOrigins restricts which Origins receive CORS headers.
	// Empty means any Origin is reflected.
	CORSAllowedOrigins []string
}

// Router is the single HTTP entry point: health check, REST routes, the MCP
// transport at /mcp and /api, and the static site, wrapped with auth, rate
// limiting, response caching and CORS.
type Router struct {
	mux *http.ServeMux
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.mux.ServeHTTP(w, r) }

// NewRouter builds the Router from cfg.
func NewRouter(cfg Config) *Router {
	rate := cfg.RateLimitPerMinute
	if rate <= 0 {
		rate = 60
	}
	auth := AuthConfig{
		AllowedKeys:     toSet(cfg.AllowedAPIKeys),
		DemoMonidAPIKey: cfg.DemoMonidAPIKey,
	}
	cors := corsConfig{allowedOrigins: toSet(cfg.CORSAllowedOrigins)}
	limiter := newRateLimiter()
	cache := newResponseCache()

	rt := &restAPI{caller: cfg.Caller}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", handleHealthz)

	// REST routes: CORS (answers OPTIONS preflight before method/auth is
	// even checked), method match, auth (caller's own Monid key), rate
	// limit, then (GET only) response cache, then the handler.
	for _, route := range restRoutes(rt) {
		handler := withCORS(cors, withMethod(route.method, requireAuth(auth, func(w http.ResponseWriter, r *http.Request, id callerIdentity) {
			bucket := id.bucketKey
			ratePerMinute := rate
			if id.isDemo {
				ratePerMinute = demoRateLimitPerMinute
			}
			if !limiter.allow(bucket, ratePerMinute) {
				writeFDError(w, http.StatusTooManyRequests, "rate_limited", "Rate limit exceeded. Retry shortly.")
				return
			}
			cached := withCache(cache, func(w http.ResponseWriter, r *http.Request) {
				route.handler(w, r, id)
			})
			cached(w, r)
		})))
		mux.HandleFunc(route.path, handler)
	}

	// MCP: mounted verbatim at both paths, in-process, never cached. CORS
	// applies; rate limiting keys off the caller's presented key (or an
	// "anonymous" bucket when none is presented — the dispatcher, not this
	// router, decides whether that key is actually authorized to call
	// Monid).
	mcpHandler := withCORS(cors, func(w http.ResponseWriter, r *http.Request) {
		bucketKey := strings.TrimSpace(r.Header.Get(apiKeyHeader))
		if bucketKey == "" {
			bucketKey = "anonymous"
		}
		if !limiter.allow(bucketKey, rate) {
			writeFDError(w, http.StatusTooManyRequests, "rate_limited", "Rate limit exceeded. Retry shortly.")
			return
		}
		cfg.MCPHandler.ServeHTTP(w, r)
	})
	for _, p := range []string{"/mcp", "/mcp/", "/api", "/api/"} {
		mux.Handle(p, mcpHandler)
	}

	site := newStaticSite(cfg.StaticDir)
	mux.Handle("/", site)

	return &Router{mux: mux}
}

// withMethod rejects any request whose method does not equal method (404,
// matching an unregistered route). OPTIONS preflight is answered by
// withCORS before this ever runs, so this only gates real requests.
func withMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func toSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			set[v] = true
		}
	}
	return set
}

// corsConfig is the reflect-origin CORS policy ported from the former
// gateway/gateway.go.
type corsConfig struct {
	// allowedOrigins restricts which Origins receive CORS headers. Nil/empty
	// means any Origin is reflected.
	allowedOrigins map[string]bool
}

func (c corsConfig) originFor(r *http.Request) (string, bool) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return "", false
	}
	if len(c.allowedOrigins) == 0 {
		return origin, true
	}
	if c.allowedOrigins[origin] {
		return origin, true
	}
	return "", false
}

// withCORS reflects an allowed Origin and answers OPTIONS preflight
// requests without reaching next.
func withCORS(cors corsConfig, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin, ok := cors.originFor(r); ok {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Headers", "Content-Type, X-API-KEY, Authorization")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next(w, r)
	}
}

// requestBaseURL returns the scheme://host this request arrived on, used to
// build absolute next_page_url links. It honors X-Forwarded-Proto, set by
// Fly's edge proxy, ahead of r.TLS.
func requestBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
}
