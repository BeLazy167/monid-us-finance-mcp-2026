// Per-call provenance: which Monid provider and endpoint answered, what
// each run cost, and how long it took. This is the same material the
// receipts ledger writes to disk, surfaced on the call itself so a caller
// can see the route without owning the ledger file. It never reaches a
// Financial Datasets response body; the REST layer emits it as a header
// only when the request opts in.
package service

import (
	"sync"
	"time"
)

// TraceStep is one Monid run, or one cache hit that stood in for it.
type TraceStep struct {
	Provider     string   `json:"provider"`
	Endpoint     string   `json:"endpoint"`
	RunID        string   `json:"run_id,omitempty"`
	Status       string   `json:"status,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	Milliseconds int64    `json:"ms"`
	// Cached is true when the 5-minute run cache answered and no Monid
	// call was made, which is why the step can cost nothing.
	Cached bool `json:"cached"`
	// Error carries the failure text when the run did not complete. The
	// caller's API key never appears in it; monid.RunError is built that
	// way deliberately.
	Error string `json:"error,omitempty"`
}

// traceRecorder collects steps from the goroutines a fan-out tool starts.
type traceRecorder struct {
	mu    sync.Mutex
	steps []TraceStep
}

func (t *traceRecorder) add(step TraceStep) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
}

// snapshot returns the steps recorded so far, oldest first by completion.
func (t *traceRecorder) snapshot() []TraceStep {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TraceStep, len(t.steps))
	copy(out, t.steps)
	return out
}

// TotalCostUSD sums what a trace's runs actually cost. Cache hits and
// failed runs contribute nothing, because Monid bills neither.
func TotalCostUSD(steps []TraceStep) float64 {
	var total float64
	for _, s := range steps {
		if s.CostUSD != nil {
			total += *s.CostUSD
		}
	}
	return total
}

func elapsedMS(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}
