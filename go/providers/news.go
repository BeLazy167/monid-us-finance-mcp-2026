// This file ports normalize.normalize_news + fd.news_record: turning a
// Context.dev /news/search payload into newest-first FD News records.
package providers

import (
	"encoding/json"

	"github.com/belazy/monid-finance/fd"
)

// newsRecordKeys is the wrapper-key search order for the article list,
// mirroring normalize.normalize_news's call to _extract_records.
var newsRecordKeys = []string{"news", "articles", "results", "data"}

// NormalizeNews parses a Context.dev /news/search payload into FD News
// records, newest first (by published_at/publishedAt/date), truncated to
// limit. Mirrors normalize.normalize_news + fd.news_record.
func NormalizeNews(raw json.RawMessage, ticker string, limit int) ([]fd.News, error) {
	records, err := extractRecords(raw, newsRecordKeys)
	if err != nil {
		return nil, err
	}
	sortRecordsByDateDesc(records)
	if limit >= 0 && len(records) > limit {
		records = records[:limit]
	}

	articles := make([]fd.News, 0, len(records))
	for _, record := range records {
		symbol := ticker
		articles = append(articles, fd.News{
			Ticker: &symbol,
			Title:  firstStringPtr(record, "title", "headline"),
			Source: firstStringPtr(record, "source"),
			Date:   firstStringPtr(record, "published_at", "publishedAt", "date"),
			URL:    firstStringPtr(record, "url"),
		})
	}
	return articles, nil
}
