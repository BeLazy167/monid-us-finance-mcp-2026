package providers

import "testing"

func TestNewsSource(t *testing.T) {
	url := "https://www.acquirersmultiple.com/2026/09/apple/"
	if got := newsSource(map[string]any{}, &url); got == nil || *got != "acquirersmultiple.com" {
		t.Fatalf("host fallback = %v, want acquirersmultiple.com", got)
	}
	stated := "Reuters"
	if got := newsSource(map[string]any{"source": stated}, &url); got == nil || *got != "Reuters" {
		t.Fatalf("a stated source must win, got %v", got)
	}
	if got := newsSource(map[string]any{}, nil); got != nil {
		t.Fatalf("with neither a source nor a URL the field stays absent, got %v", *got)
	}
}
