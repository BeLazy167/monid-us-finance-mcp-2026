package httpapi

import (
	"container/list"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cache sizing and TTL policy. TTLs mirror the volatility of each data
// class: prices/snapshot/news are fast-moving, financial statements change
// at most daily, and everything else is a middle-ground. Ported unchanged
// from the former gateway/cache.go.
const (
	maxCacheEntries  = 4096
	ttlPricesAndNews = 60 * time.Second
	ttlFinancials    = 600 * time.Second
	ttlDefault       = 300 * time.Second
)

// cacheStore is where a cached response body lives.
//
// The in-memory store is the default and needs no configuration. A shared
// store is worth having because this one is not shared and not durable:
// every machine keeps its own, and a deploy throws all of them away. That
// is the whole reason a first ask stays slow no matter how much traffic
// this server has already answered. A miss and a broken backend are the
// same answer here, because a cache that cannot be reached must cost the
// caller a slower response and never an error.
type cacheStore interface {
	get(key string, now time.Time) (cacheEntry, bool)
	put(key string, entry cacheEntry)
}

// cacheEntry is a single cached response body.
type cacheEntry struct {
	body        []byte
	contentType string
	status      int
	expiresAt   time.Time
}

// responseCache is a bounded, FIFO (oldest-first) eviction response cache.
// It is safe for concurrent use.
type responseCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List // insertion order; front is the oldest entry
}

type cacheItem struct {
	key   string
	entry cacheEntry
}

func newResponseCache() *responseCache {
	return &responseCache{
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// get returns a cached response if present and not yet expired.
func (c *responseCache) get(key string, now time.Time) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	item := el.Value.(*cacheItem)
	if now.After(item.entry.expiresAt) {
		c.removeLocked(el)
		return cacheEntry{}, false
	}
	return item.entry, true
}

// put stores a response under key, evicting the oldest entry when the cache
// has grown beyond maxCacheEntries. Re-putting an existing key updates it in
// place without changing its eviction order.
func (c *responseCache) put(key string, entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*cacheItem).entry = entry
		return
	}
	c.entries[key] = c.order.PushBack(&cacheItem{key: key, entry: entry})
	for c.order.Len() > maxCacheEntries {
		c.removeLocked(c.order.Front())
	}
}

func (c *responseCache) removeLocked(el *list.Element) {
	item := el.Value.(*cacheItem)
	delete(c.entries, item.key)
	c.order.Remove(el)
}

// cacheKey builds a stable cache key from the request path plus its query
// parameters sorted by name (and value) so order-independent queries share
// one entry. The caller identity is folded in so two callers never share a
// cached response for a keyed route.
func cacheKey(r *http.Request, identity string) string {
	q := r.URL.Query()
	var sb strings.Builder
	sb.WriteString(identity)
	sb.WriteByte('|')
	sb.WriteString(r.URL.Path)
	if len(q) == 0 {
		return sb.String()
	}
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	sort.Strings(names)

	sb.WriteByte('?')
	first := true
	for _, name := range names {
		values := append([]string(nil), q[name]...)
		sort.Strings(values)
		for _, v := range values {
			if !first {
				sb.WriteByte('&')
			}
			first = false
			sb.WriteString(url.QueryEscape(name))
			sb.WriteByte('=')
			sb.WriteString(url.QueryEscape(v))
		}
	}
	return sb.String()
}

// ttlForPath returns the cache TTL for a REST path per the TTL policy.
func ttlForPath(p string) time.Duration {
	switch {
	case strings.Contains(p, "/prices") || strings.Contains(p, "/snapshot") || strings.Contains(p, "/news"):
		return ttlPricesAndNews
	case strings.Contains(p, "/financials/") || strings.Contains(p, "/filings") || strings.Contains(p, "/financial-metrics"):
		return ttlFinancials
	default:
		return ttlDefault
	}
}

// cachingResponseWriter tees the response to the client while buffering a
// copy for the response cache.
type cachingResponseWriter struct {
	http.ResponseWriter
	status int
	body   []byte
}

func (w *cachingResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *cachingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	w.body = append(w.body, b...)
	return w.ResponseWriter.Write(b)
}

// withCache serves a cached GET response when present, else runs next and
// caches a successful (status < 400) response body. /mcp and /api are never
// routed through this: their handler is registered separately.
func withCache(cache cacheStore, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A request asking for provenance must reach the handler: a
		// replayed body carries no route, and the point of asking is to
		// see the route. The service-level run cache still spares the
		// Monid calls, and reports each spared call as a cached step.
		if r.Method != http.MethodGet || wantsTrace(r) {
			next(w, r)
			return
		}
		identity := r.Header.Get(apiKeyHeader)
		key := cacheKey(r, identity)
		if entry, hit := cache.get(key, time.Now()); hit {
			w.Header().Set("X-Cache", "hit")
			w.Header().Set("Content-Type", entry.contentType)
			w.WriteHeader(entry.status)
			_, _ = w.Write(entry.body)
			return
		}

		w.Header().Set("X-Cache", "miss")
		tw := &cachingResponseWriter{ResponseWriter: w}
		next(tw, r)
		if tw.status > 0 && tw.status < http.StatusBadRequest {
			cache.put(key, cacheEntry{
				body:        tw.body,
				contentType: w.Header().Get("Content-Type"),
				status:      tw.status,
				expiresAt:   time.Now().Add(ttlForPath(r.URL.Path)),
			})
		}
	}
}
