package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// countingHandler answers a fixed body and records how many times it ran.
func countingHandler(body string, ran *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*ran++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

func askAs(t *testing.T, handler http.HandlerFunc, apiKey, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set(apiKeyHeader, apiKey)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// TestCacheIsSharedAcrossCallers is the point of sharing: the second
// caller is answered from what the first one paid for, so a provider is
// called once for an answer that is the same whoever asks.
func TestCacheIsSharedAcrossCallers(t *testing.T) {
	ran := 0
	handler := withCache(newResponseCache(), false, countingHandler(`{"income_statements":[]}`, &ran))
	target := "/financials/income-statements?ticker=AAPL&period=annual"

	first := askAs(t, handler, "monid_live_aaa", target)
	if first.Header().Get("X-Cache") != "miss" {
		t.Fatalf("first ask was %q, want a miss", first.Header().Get("X-Cache"))
	}
	second := askAs(t, handler, "monid_live_bbb", target)
	if second.Header().Get("X-Cache") != "hit" {
		t.Fatalf("a different caller was %q, want a hit on the first caller's entry",
			second.Header().Get("X-Cache"))
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("the two callers were given different bodies:\n  %s\n  %s",
			first.Body.String(), second.Body.String())
	}
	if ran != 1 {
		t.Fatalf("the handler ran %d times for one answer, want 1", ran)
	}
}

// TestCachePerCallerIsolates checks an operator can still keep callers
// from benefiting from each other's spend.
func TestCachePerCallerIsolates(t *testing.T) {
	ran := 0
	handler := withCache(newResponseCache(), true, countingHandler(`{"income_statements":[]}`, &ran))
	target := "/financials/income-statements?ticker=AAPL&period=annual"

	askAs(t, handler, "monid_live_aaa", target)
	second := askAs(t, handler, "monid_live_bbb", target)
	if second.Header().Get("X-Cache") != "miss" {
		t.Fatalf("a second caller was %q, want a miss when entries are per caller",
			second.Header().Get("X-Cache"))
	}
	if ran != 2 {
		t.Fatalf("the handler ran %d times for two callers, want 2", ran)
	}
	// The same caller still gets its own entry back.
	if again := askAs(t, handler, "monid_live_aaa", target); again.Header().Get("X-Cache") != "hit" {
		t.Fatalf("the first caller was %q on its own entry, want a hit", again.Header().Get("X-Cache"))
	}
}

// TestSharedCacheStillSeparatesDifferentQuestions checks sharing folds
// callers together and nothing else.
func TestSharedCacheStillSeparatesDifferentQuestions(t *testing.T) {
	ran := 0
	handler := withCache(newResponseCache(), false, countingHandler(`{}`, &ran))
	askAs(t, handler, "a", "/financials/income-statements?ticker=AAPL&period=annual")
	askAs(t, handler, "b", "/financials/income-statements?ticker=MSFT&period=annual")
	if ran != 2 {
		t.Fatalf("two different tickers ran the handler %d times, want 2", ran)
	}
	// Query order must not matter, which is what sorting the key is for.
	askAs(t, handler, "c", "/financials/income-statements?period=annual&ticker=AAPL")
	if ran != 2 {
		t.Fatalf("the same question written in another order ran the handler again (%d)", ran)
	}
}
