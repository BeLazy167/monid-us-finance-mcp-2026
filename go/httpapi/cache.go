package httpapi

import (
	"container/list"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	// ttlSettled is how long an answer about a period that has already
	// closed is kept. Ten minutes is the right ceiling for "the latest
	// figures", and the wrong one for a quarter that ended last year: the
	// answer cannot change, so expiring it only makes the next caller pay
	// a provider again to be told the same thing. Measured 2026-09-06, a
	// cold ask took a median 5,256ms against 69ms once cached.
	//
	// A day rather than forever, because a company can restate a closed
	// period in a later filing. A restatement is rare and this bounds how
	// long one would go unnoticed.
	ttlSettled = 24 * time.Hour
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

// settledDateParams bound a request above, to a period that has closed.
// A parameter that only bounds it below (start_date, report_period_gte)
// leaves the newest period in the answer, which has not.
var settledDateParams = []string{
	"end_date", "report_period", "report_period_lte", "report_period_lt",
	"filing_date", "filing_date_lte", "filing_date_lt",
}

// namesSettledPeriod reports whether a request can only be answered with
// figures that are already fixed.
func namesSettledPeriod(q url.Values, now time.Time) bool {
	// A trailing-twelve-month row is priced at the latest close whatever
	// else the request asks for (see service/valuation.go), so it always
	// carries a live figure and is never settled.
	if strings.EqualFold(strings.TrimSpace(q.Get("period")), "ttm") {
		return false
	}
	// An accession number names one filed document, and a filed document
	// does not change.
	if strings.TrimSpace(q.Get("accession_number")) != "" {
		return true
	}
	today := now.UTC().Truncate(24 * time.Hour)
	for _, name := range settledDateParams {
		value := strings.TrimSpace(q.Get(name))
		if value == "" {
			continue
		}
		day, err := time.Parse("2006-01-02", value)
		if err == nil && day.Before(today) {
			return true
		}
	}
	if raw := strings.TrimSpace(q.Get("year")); raw != "" {
		if year, err := strconv.Atoi(raw); err == nil && year < now.UTC().Year() {
			return true
		}
	}
	return false
}

// ttlForRequest returns how long one request's answer may be cached.
func ttlForRequest(r *http.Request, now time.Time) time.Duration {
	if namesSettledPeriod(r.URL.Query(), now) {
		return ttlSettled
	}
	return ttlForPath(r.URL.Path)
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
				expiresAt:   time.Now().Add(ttlForRequest(r, time.Now())),
			})
		}
	}
}
