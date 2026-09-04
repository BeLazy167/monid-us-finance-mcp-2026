package fd

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNewErrorResponse ports
// tests/test_compat_receipts.py::test_fd_error_matches_documented_shape.
func TestNewErrorResponse(t *testing.T) {
	resp := NewErrorResponse("not_found", "No data found")
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"error":"not_found","message":"No data found"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

// TestCursorRoundTrip ports
// tests/test_compat_receipts.py::test_cursor_round_trip.
func TestCursorRoundTrip(t *testing.T) {
	cursor := EncodeCursor(10, map[string]any{"ticker": "AAPL"})
	offset, filters, err := DecodeCursor(cursor)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if offset != 10 {
		t.Fatalf("got offset=%d, want 10", offset)
	}
	if filters["ticker"] != "AAPL" {
		t.Fatalf("got filters=%v, want ticker=AAPL", filters)
	}
}

// TestDecodeCursor_RejectsMalformedTokens ports
// tests/test_compat_receipts.py::test_cursor_rejects_malformed_tokens.
func TestDecodeCursor_RejectsMalformedTokens(t *testing.T) {
	for _, cursor := range []string{"!!!", "eyJvIjogLTF9", "e30", "bnVsbA"} {
		t.Run(cursor, func(t *testing.T) {
			if _, _, err := DecodeCursor(cursor); err == nil {
				t.Fatalf("expected a CursorError for %q", cursor)
			} else if _, ok := err.(*CursorError); !ok {
				t.Fatalf("expected *CursorError, got %T", err)
			}
		})
	}
}

// TestPaginate_EmitsNextOnlyWhenMoreRemain ports
// tests/test_compat_receipts.py::test_paginate_emits_next_only_when_more_remain.
func TestPaginate_EmitsNextOnlyWhenMoreRemain(t *testing.T) {
	records := make([]map[string]int, 25)
	for i := range records {
		records[i] = map[string]int{"i": i}
	}
	first, err := Paginate(records, 0, DefaultPageSize, "/filings", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 10 {
		t.Fatalf("got %d records, want 10", len(first.Records))
	}
	if first.NextCursor == nil || first.NextURL == nil {
		t.Fatal("expected a next cursor and next url")
	}
	if !strings.HasPrefix(*first.NextURL, CompatBaseURL+"/filings?cursor=") {
		t.Fatalf("got next url %q, want prefix %s/filings?cursor=", *first.NextURL, CompatBaseURL)
	}
	secondOffset, _, err := DecodeCursor(*first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Paginate(records, secondOffset, DefaultPageSize, "/filings", nil)
	if err != nil {
		t.Fatal(err)
	}
	if secondOffset != 10 {
		t.Fatalf("got secondOffset=%d, want 10", secondOffset)
	}
	if len(second.Records) != 10 {
		t.Fatalf("got %d records, want 10", len(second.Records))
	}
	thirdOffset, _, err := DecodeCursor(*second.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	third, err := Paginate(records, thirdOffset, DefaultPageSize, "/filings", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Records) != 5 {
		t.Fatalf("got %d records, want 5", len(third.Records))
	}
	if third.NextCursor != nil || third.NextURL != nil {
		t.Fatal("expected no next cursor/url on the last page")
	}
}

func TestPaginate_RejectsNonPositivePageSize(t *testing.T) {
	if _, err := Paginate([]int{1, 2, 3}, 0, 0, "/x", nil); err == nil {
		t.Fatal("expected an error for page_size < 1")
	}
}

func TestPaginate_PricesPageSize(t *testing.T) {
	records := make([]Price, 150)
	page, err := Paginate(records, 0, PricesPageSize, "/prices", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 100 {
		t.Fatalf("got %d records, want 100", len(page.Records))
	}
	if page.NextURL == nil {
		t.Fatal("expected a next url for the remaining 50 records")
	}
}

// TestListResponse_KeyOrder pins the wrapped-list envelope's JSON key order
// (named key first, next_page_url last), mirroring fd.list_response.
func TestListResponse_KeyOrder(t *testing.T) {
	url := "https://example.com/next"
	resp := NewListResponse("filings", []map[string]any{{"a": 1}}, &url)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"filings":[{"a":1}],"next_page_url":"https://example.com/next"}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestListResponse_OmitsNextPageURLWhenNil(t *testing.T) {
	resp := NewListResponse("news", []map[string]any{}, nil)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"news":[]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestPricesResponse_KeyOrderAndOmission(t *testing.T) {
	open := 30.0
	resp := NewPricesResponse("AAPL", []Price{{Open: &open}}, nil)
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"ticker":"AAPL","prices":[{"open":30}]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestPriceSnapshotResponse_Wraps(t *testing.T) {
	price := 230.1
	ticker := "AAPL"
	resp := NewPriceSnapshotResponse(PriceSnapshot{Price: &price, Ticker: &ticker})
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"snapshot":{"price":230.1,"ticker":"AAPL"}}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestFinancialMetricSnapshotResponse_Wraps(t *testing.T) {
	ticker := "AAPL"
	resp := NewFinancialMetricSnapshotResponse(FinancialMetricSnapshot{Ticker: &ticker})
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"snapshot":{"ticker":"AAPL"}}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}

func TestFinancialsSearchResponse_NoNextPageURLField(t *testing.T) {
	resp := NewFinancialsSearchResponse([]map[string]string{{"ticker": "AAPL"}})
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"search_results":[{"ticker":"AAPL"}]}`
	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}
