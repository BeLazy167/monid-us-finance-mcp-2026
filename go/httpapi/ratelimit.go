package httpapi

import (
	"sync"
	"time"
)

// bucket is a token bucket for a single rate-limit key.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// allow consumes one token if a full token is available, refilling toward
// capacity at the per-minute rate. Safe for concurrent use.
func (b *bucket) allow(ratePerMinute int) bool {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()

	capacity := float64(ratePerMinute)
	refill := now.Sub(b.last).Seconds() * capacity / 60.0
	b.tokens += refill
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.last = now

	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// rateLimiter holds one token bucket per rate-limit key. Ported unchanged
// from the former gateway/ratelimit.go.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*bucket)}
}

// allow takes a token from the bucket for key, creating it full on first use.
func (rl *rateLimiter) allow(key string, ratePerMinute int) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(ratePerMinute), last: time.Now()}
		rl.buckets[key] = b
	}
	rl.mu.Unlock()
	return b.allow(ratePerMinute)
}
