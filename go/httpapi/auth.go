package httpapi

import (
	"net/http"
	"strings"
)

// apiKeyHeader is the header carrying the caller's own Monid API key. Its
// value is passed straight through to Caller.Call so the caller's Monid
// wallet pays for its own usage. It is never logged.
const apiKeyHeader = "X-API-KEY"

const unauthorizedMessage = "Missing or invalid API key."

// demoTickers are the only tickers a keyless request may read, matching
// Financial Datasets' own instant-tryout tickers.
var demoTickers = map[string]bool{"AAPL": true, "MSFT": true, "NVDA": true}

// demoRateLimitPerMinute is the shared bucket rate for keyless demo
// traffic. It is intentionally stricter than the default per-key rate so
// one demo caller cannot crowd out authenticated traffic.
const demoRateLimitPerMinute = 10

// demoBucketKey is the shared rate-limit identity for keyless demo requests.
const demoBucketKey = "demo"

// AuthConfig is the caller-key policy: pass the caller's own Monid API key
// through, optionally restricted to an allowlist, with an optional keyless
// demo mode.
type AuthConfig struct {
	// AllowedKeys optionally restricts which caller-supplied keys may pass.
	// Empty/nil means any well-formed, non-empty key is accepted (this is
	// NOT a server-side key allowlist gating access to a shared backend key
	// — the caller's key is always their own Monid key).
	AllowedKeys map[string]bool

	// DemoMonidAPIKey, when non-empty, enables keyless GETs for
	// demoTickers: those requests use this key (a Monid key the operator
	// funds) under the stricter demo rate-limit bucket. When empty, keyless
	// requests are always unauthorized.
	DemoMonidAPIKey string
}

// callerIdentity is the resolved caller for one request.
type callerIdentity struct {
	// monidAPIKey is the key forwarded to Caller.Call.
	monidAPIKey string
	// bucketKey is the rate-limit identity.
	bucketKey string
	// isDemo marks a keyless demo-mode request.
	isDemo bool
}

// authorize resolves the caller identity for r, or reports that the request
// must be rejected with 401. It never logs the presented key.
func (cfg AuthConfig) authorize(r *http.Request) (callerIdentity, bool) {
	if provided := strings.TrimSpace(r.Header.Get(apiKeyHeader)); provided != "" {
		if !isWellFormedAPIKey(provided) {
			return callerIdentity{}, false
		}
		if len(cfg.AllowedKeys) > 0 && !cfg.AllowedKeys[provided] {
			return callerIdentity{}, false
		}
		return callerIdentity{monidAPIKey: provided, bucketKey: provided}, true
	}

	// Keyless demo mode: only GETs for a demo ticker, only when configured.
	if cfg.DemoMonidAPIKey != "" && r.Method == http.MethodGet {
		ticker := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("ticker")))
		if demoTickers[ticker] {
			return callerIdentity{
				monidAPIKey: cfg.DemoMonidAPIKey,
				bucketKey:   demoBucketKey,
				isDemo:      true,
			}, true
		}
	}

	return callerIdentity{}, false
}

// isWellFormedAPIKey rejects control characters (including CR/LF, which
// would otherwise let a malformed header value smuggle extra headers into
// any downstream request built from it).
func isWellFormedAPIKey(key string) bool {
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// requireAuth wraps next with the caller-key gate. On success it stores the
// resolved identity in the request context for downstream handlers (rate
// limiting, service calls).
func requireAuth(cfg AuthConfig, next func(w http.ResponseWriter, r *http.Request, id callerIdentity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := cfg.authorize(r)
		if !ok {
			writeFDError(w, http.StatusUnauthorized, "unauthorized", unauthorizedMessage)
			return
		}
		next(w, r, id)
	}
}
