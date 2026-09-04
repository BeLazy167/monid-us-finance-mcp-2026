package providers

import (
	"encoding/json"
	"testing"
)

// newsFixture mirrors tests/test_service.py's NEWS constant.
const newsFixture = `{
	"data": [
		{"id": "old", "title": "Old", "published_at": "2026-01-01T12:00:00Z",
		 "url": "https://example.com/old", "source": "example.com"},
		{"id": "new", "title": "New", "published_at": "2026-02-01T12:00:00Z",
		 "url": "https://example.com/new", "source": "example.com"}
	],
	"has_more": false,
	"next_cursor": null
}`

// TestNormalizeNews_Shape ports
// tests/test_service.py::test_news_shape.
func TestNormalizeNews_Shape(t *testing.T) {
	records, err := NormalizeNews(json.RawMessage(newsFixture), "AAPL", 5)
	if err != nil {
		t.Fatalf("NormalizeNews: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	newest := records[0]
	assertStrPtr(t, "ticker", newest.Ticker, "AAPL")
	assertStrPtr(t, "title", newest.Title, "New")
	assertStrPtr(t, "source", newest.Source, "example.com")
	assertStrPtr(t, "date", newest.Date, "2026-02-01T12:00:00Z")
	assertStrPtr(t, "url", newest.URL, "https://example.com/new")

	oldest := records[1]
	assertStrPtr(t, "title", oldest.Title, "Old")
}

func TestNormalizeNews_Limit(t *testing.T) {
	records, err := NormalizeNews(json.RawMessage(newsFixture), "AAPL", 1)
	if err != nil {
		t.Fatalf("NormalizeNews: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	assertStrPtr(t, "title", records[0].Title, "New")
}

func TestNormalizeNews_AliasKeysAndWrapperShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"top-level array", `[{"headline": "H", "publishedAt": "2026-01-01T00:00:00Z", "url": "u", "source": "s"}]`},
		{"articles wrapper", `{"articles": [{"title": "H", "date": "2026-01-01", "url": "u", "source": "s"}]}`},
		{"results wrapper", `{"results": [{"title": "H", "published_at": "2026-01-01", "url": "u", "source": "s"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records, err := NormalizeNews(json.RawMessage(tc.raw), "AAPL", 5)
			if err != nil {
				t.Fatalf("NormalizeNews: %v", err)
			}
			if len(records) != 1 {
				t.Fatalf("got %d records, want 1", len(records))
			}
			assertStrPtr(t, "title", records[0].Title, "H")
		})
	}
}

func TestNormalizeNews_InvalidPayload(t *testing.T) {
	_, err := NormalizeNews(json.RawMessage(`{"unexpected": 1}`), "AAPL", 5)
	if err == nil {
		t.Fatal("expected a schema-drift error for a payload without a record list")
	}
}
