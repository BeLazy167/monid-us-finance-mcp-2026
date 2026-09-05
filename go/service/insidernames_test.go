package service

import (
	"context"
	"testing"
)

// insiderResult is one row of the SECForm4 search feed.
func insiderResult(relationship, date string) map[string]any {
	return map[string]any{
		"transaction_date": date + " Sale", "reported_datetime": "2026-09-03 6:30 pm",
		"company": "Apple Inc.", "symbol": "AAPL", "insider_relationship": relationship,
		"shares_traded": "1,439", "average_price": "$317.01", "total_amount": "$456,177",
		"shares_owned": "35,790 (Direct)",
	}
}

// TestListInsiderNames_DistinctAndSorted checks the directory names each
// insider once, in a stable order, however many times they traded.
func TestListInsiderNames_DistinctAndSorted(t *testing.T) {
	payload := map[string]any{"status": "success", "data": map[string]any{
		"query": "AAPL",
		"results": []any{
			insiderResult("Williams Jeffrey E COO", "2026-09-01"),
			insiderResult("Cook Timothy D CEO", "2026-08-20"),
			// The same insider trading twice must not be named twice.
			insiderResult("Cook Timothy D CEO", "2026-07-15"),
			insiderResult("Adams Katherine L SVP", "2026-06-02"),
		},
	}}
	svc, _ := newTestService(t, map[string]fakeOutcome{"secform4 /search": {output: payload}})

	result, err := svc.ListInsiderNames(context.Background(), "key", map[string]any{"ticker": "AAPL"})
	if err != nil {
		t.Fatalf("ListInsiderNames: %v", err)
	}
	body, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("value is %T, want a map naming the directory", result.Value)
	}
	names, ok := body["names"].([]string)
	if !ok {
		t.Fatalf("names is %T, want a list of strings", body["names"])
	}
	if len(names) != 3 {
		t.Fatalf("got %d names, want 3 distinct insiders: %v", len(names), names)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("names are not in order: %v", names)
		}
	}
}

// TestListInsiderNames_TickerRequired checks the directory refuses a
// request it has no issuer for, before it costs the caller a Monid call.
func TestListInsiderNames_TickerRequired(t *testing.T) {
	svc, transport := newTestService(t, map[string]fakeOutcome{})
	if _, err := svc.ListInsiderNames(context.Background(), "key", map[string]any{}); err == nil {
		t.Fatal("a request with no ticker was accepted")
	}
	if len(transport.calls) != 0 {
		t.Fatalf("a rejected request cost %d provider calls, want 0", len(transport.calls))
	}
}
