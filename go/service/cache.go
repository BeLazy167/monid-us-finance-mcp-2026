// Package service is the orchestration layer for all 27 Financial Datasets
// tools: it holds the tool -> provider/endpoint map, argument validation and
// FD-schema defaults, the honest rejections, and the composition steps that
// join multiple Monid calls into one Financial Datasets response.
//
// This file ports cache.py: a bounded, concurrency-safe TTL cache of Monid
// run results, keyed by provider+endpoint+sorted input. A cache hit performs
// no run, spends nothing, and writes no ledger row.
//
// Market data behind one provider+endpoint+input triple is identical
// regardless of which caller's API key asked for it, so this cache is
// shared across every caller deliberately: two callers asking the same
// question in the same TTL window share one Monid run and one bill, not
// two. The cache never stores or is keyed by an API key.
package service

import (
	"container/list"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/belazy/monid-finance/monid"
)

// defaultCacheTTL is the fallback TTL for endpoints with no specific policy,
// mirroring cache.DEFAULT_TTL_SECONDS.
const defaultCacheTTL = 300 * time.Second

// ttlByEndpoint is the per-endpoint TTL policy, mirroring cache.TTL_BY_ENDPOINT.
//
// companies-list gets a much longer TTL than every other endpoint: it is
// the full US ticker catalog (3,227 records, measured $0.0006/call), the
// backing source for every list*Tickers coverage list (coverage.go), and
// changes on the order of days (new listings/delistings), not minutes.
var ttlByEndpoint = map[string]time.Duration{
	"/equities/v1/companies-list": 24 * time.Hour,
	"/equities/v1/statements":     600 * time.Second,
	"/equities/v1/filings":        600 * time.Second,
	"/equities/v1/summary":        60 * time.Second,
	"/equities/v1/ohlcv":          60 * time.Second,
	"/news/search":                60 * time.Second,
	"/web/scrape/markdown":        3600 * time.Second,
	"/web/extract":                3600 * time.Second,
	"/web/search":                 600 * time.Second,
	"/search":                     600 * time.Second,
	"/get_stock_screener":         600 * time.Second,
	"/get_earnings_calendar":      300 * time.Second,
	"/get_institution_holders":    600 * time.Second,
	// The 13D/13G feed runs months stale (see docs/compatibility.md), so a
	// long TTL costs no real freshness and saves repeat callers a $0.01 run.
	"/get_13d_filings":                     3600 * time.Second,
	"/get_13g_filings":                     3600 * time.Second,
	"/get_company_insider_trading":         600 * time.Second,
	"/get_hedge_fund_portfolio":            600 * time.Second,
	"/get_ticker_sectors_with_performance": 3600 * time.Second,
	"/get_ipo_calendar":                    300 * time.Second,
	// Market movers/indices are explicitly current (as-of timestamped), so
	// this TTL stays short, unlike the ownership-state endpoints above.
	"/get_market_movers":  60 * time.Second,
	"/get_market_indices": 60 * time.Second,
}

// cacheTTLFor returns the TTL for one endpoint: exact match first, then the
// default, mirroring cache.cache_ttl_for.
func cacheTTLFor(endpoint string) time.Duration {
	if ttl, ok := ttlByEndpoint[endpoint]; ok {
		return ttl
	}
	return defaultCacheTTL
}

// cacheKey is a hashable key for one Monid call: provider, endpoint, plus
// the sorted-key JSON encoding of body and query params (or "" when nil),
// mirroring service._cache_key. Two calls with the same provider, endpoint,
// body, and query params - regardless of caller - collide on this key by
// design.
type cacheKey struct {
	provider    string
	endpoint    string
	body        string
	queryParams string
}

// newCacheKey builds one cacheKey, sorting map keys the way Python's
// json.dumps(..., sort_keys=True) does. A nil or empty map encodes as "".
func newCacheKey(provider, endpoint string, body, queryParams map[string]any) cacheKey {
	return cacheKey{
		provider:    provider,
		endpoint:    endpoint,
		body:        sortedJSON(body),
		queryParams: sortedJSON(queryParams),
	}
}

// sortedJSON renders a map as compact JSON with keys in sorted order,
// matching json.dumps(value, sort_keys=True, separators=(",", ":")). An
// empty or nil map renders as "" (not "{}"), mirroring
// service._cache_key's `if body else ""` guard.
func sortedJSON(value map[string]any) string {
	if len(value) == 0 {
		return ""
	}
	keys := make([]string, 0, len(value))
	for k := range value {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make([]byte, 0, 256)
	ordered = append(ordered, '{')
	for i, k := range keys {
		if i > 0 {
			ordered = append(ordered, ',')
		}
		keyJSON, _ := json.Marshal(k)
		ordered = append(ordered, keyJSON...)
		ordered = append(ordered, ':')
		valueJSON, err := json.Marshal(value[k])
		if err != nil {
			valueJSON = []byte("null")
		}
		ordered = append(ordered, valueJSON...)
	}
	ordered = append(ordered, '}')
	return string(ordered)
}

// runCacheEntry is one cached run plus its absolute expiry.
type runCacheEntry struct {
	key       cacheKey
	expiresAt time.Time
	run       *monid.Run
}

// RunCache is a bounded, per-entry-TTL LRU cache of Monid run results,
// mirroring cache.RunCache. It is safe for concurrent use: every method
// takes an internal mutex, matching cache.py's single-threaded-asyncio
// semantics (never interleaved) with Go's actual concurrency (goroutines
// racing to Get/Put during the service's parallel fan-outs).
type RunCache struct {
	mu         sync.Mutex
	maxEntries int
	entries    map[cacheKey]*list.Element // list.Element.Value is *runCacheEntry
	order      *list.List                 // front = most recently used
	Hits       int
	Misses     int
}

// NewRunCache returns a RunCache bounded to maxEntries, mirroring
// RunCache.__init__. maxEntries < 1 is treated as 1 (the Python
// constructor raises ValueError instead; a Go constructor returning an
// error for a static, code-controlled configuration value would only push
// the same mistake to every caller, so this clamps instead).
func NewRunCache(maxEntries int) *RunCache {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &RunCache{
		maxEntries: maxEntries,
		entries:    make(map[cacheKey]*list.Element),
		order:      list.New(),
	}
}

// Get returns the cached run for key if present and not expired, mirroring
// RunCache.get. An expired entry is evicted and counted as a miss.
func (c *RunCache) Get(key cacheKey) (*monid.Run, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.entries[key]
	if !ok {
		c.Misses++
		return nil, false
	}
	entry := elem.Value.(*runCacheEntry)
	if !entry.expiresAt.After(time.Now()) {
		c.order.Remove(elem)
		delete(c.entries, key)
		c.Misses++
		return nil, false
	}
	c.order.MoveToFront(elem)
	c.Hits++
	return entry.run, true
}

// Put stores run under key with the given ttl, mirroring RunCache.put.
// ttl <= 0 stores nothing (a zero-or-negative TTL means "never cache this
// endpoint"; cacheTTLFor never returns that today, but Put stays defensive).
// Storing over an existing key's capacity evicts the least-recently-used
// entry until the cache is back within maxEntries.
func (c *RunCache) Put(key cacheKey, run *monid.Run, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := &runCacheEntry{key: key, expiresAt: time.Now().Add(ttl), run: run}
	if elem, ok := c.entries[key]; ok {
		elem.Value = entry
		c.order.MoveToFront(elem)
	} else {
		elem := c.order.PushFront(entry)
		c.entries[key] = elem
	}
	for len(c.entries) > c.maxEntries {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*runCacheEntry).key)
	}
}

// Clear empties the cache, mirroring RunCache.clear.
func (c *RunCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[cacheKey]*list.Element)
	c.order = list.New()
}

// Len reports the current entry count, mirroring RunCache.__len__.
func (c *RunCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
