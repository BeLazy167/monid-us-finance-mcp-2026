// This file ports compat.py's Financial Datasets response-envelope
// helpers: the ErrorResponse builder, opaque base64url pagination cursors,
// local-slice pagination, and the wrapped list/prices/snapshot response
// shapes used by the filings, prices, news, summary, screener, and insider
// providers.
//
// fd.ErrorResponse itself already lives in types.go; this file only adds
// the constructor and the envelope types the contract does not otherwise
// provide (see the Financial Datasets contract's FilingsResponse,
// PricesResponse, PriceSnapshotResponse, NewsResponse,
// InsiderTradesResponse, FinancialMetricSnapshotResponse, and
// FinancialsSearchResponse schemas).
package fd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strings"
)

// CompatBaseURL is the fixed authority used to build next_page_url values
// in this compatibility layer; it never needs to be independently
// resolvable, mirroring compat.COMPAT_BASE_URL.
const CompatBaseURL = "https://api.monid-finance-mcp.example"

// DefaultPageSize is the page size for every locally paginated list
// response except prices, mirroring compat.DEFAULT_PAGE_SIZE.
const DefaultPageSize = 10

// PricesPageSize is the page size for the prices list response, mirroring
// compat.PRICES_PAGE_SIZE.
const PricesPageSize = 100

// NewErrorResponse builds the Financial Datasets ErrorResponse shape
// {"error": code, "message": message}, mirroring compat.fd_error.
func NewErrorResponse(code, message string) ErrorResponse {
	c, m := code, message
	return ErrorResponse{Error: &c, Message: &m}
}

// CursorError reports a caller-supplied cursor that is not a valid opaque
// cursor minted by this server, mirroring compat.CursorError.
type CursorError struct{ Msg string }

func (e *CursorError) Error() string { return e.Msg }

// cursorPayload is the opaque cursor's decoded shape: the next offset and
// the filter set it was generated under.
type cursorPayload struct {
	Offset  int            `json:"o"`
	Filters map[string]any `json:"f"`
}

// EncodeCursor mints an opaque, base64url pagination cursor carrying the
// next offset and the filter set it was generated under, mirroring
// compat.encode_cursor. A nil filters map encodes as {}.
func EncodeCursor(offset int, filters map[string]any) string {
	if filters == nil {
		filters = map[string]any{}
	}
	// encoding/json marshals map[string]any keys in sorted order, matching
	// Python's json.dumps(..., sort_keys=True).
	raw, err := json.Marshal(cursorPayload{Offset: offset, Filters: filters})
	if err != nil {
		raw = []byte(`{"o":0,"f":{}}`)
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(raw), "=")
}

// DecodeCursor reverses EncodeCursor, rejecting anything not shaped like a
// cursor this server would have minted, mirroring compat.decode_cursor.
func DecodeCursor(cursor string) (int, map[string]any, error) {
	padded := cursor + strings.Repeat("=", (4-len(cursor)%4)%4)
	raw, err := base64.URLEncoding.DecodeString(padded)
	if err != nil {
		return 0, nil, &CursorError{Msg: "cursor is not a valid opaque pagination token"}
	}
	var loaded any
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return 0, nil, &CursorError{Msg: "cursor is not a valid opaque pagination token"}
	}
	payload, ok := loaded.(map[string]any)
	if !ok {
		return 0, nil, &CursorError{Msg: "cursor payload must be an object"}
	}
	offsetValue, exists := payload["o"]
	offsetFloat, isNumber := offsetValue.(float64)
	if !exists || !isNumber || offsetFloat < 0 || offsetFloat != math.Trunc(offsetFloat) {
		return 0, nil, &CursorError{Msg: "cursor offset must be a non-negative integer"}
	}
	filtersValue, exists := payload["f"]
	filters, isObject := filtersValue.(map[string]any)
	if !exists || !isObject {
		return 0, nil, &CursorError{Msg: "cursor filters must be an object with string keys"}
	}
	return int(offsetFloat), filters, nil
}

// NextPageURL builds the opaque continuation URL for one path + cursor,
// mirroring compat.next_page_url.
func NextPageURL(path, cursor string) string {
	values := url.Values{}
	values.Set("cursor", cursor)
	return CompatBaseURL + path + "?" + values.Encode()
}

// Page is one Financial Datasets style page of a locally filtered result
// set, mirroring compat.Page.
type Page[T any] struct {
	Records    []T
	NextCursor *string
	NextURL    *string
}

// Paginate slices a filtered record list into one page plus an opaque
// continuation, mirroring compat.paginate.
func Paginate[T any](records []T, offset, pageSize int, path string, filtersForCursor map[string]any) (Page[T], error) {
	if pageSize < 1 {
		return Page[T]{}, errors.New("page_size must be positive")
	}
	if offset < 0 {
		offset = 0
	}
	start := offset
	if start > len(records) {
		start = len(records)
	}
	end := offset + pageSize
	if end > len(records) {
		end = len(records)
	}
	page := append([]T{}, records[start:end]...)
	nextOffset := offset + pageSize
	if nextOffset < len(records) && len(page) > 0 {
		cursor := EncodeCursor(nextOffset, filtersForCursor)
		next := NextPageURL(path, cursor)
		return Page[T]{Records: page, NextCursor: &cursor, NextURL: &next}, nil
	}
	return Page[T]{Records: page}, nil
}

// ListResponse is the generic Financial Datasets wrapped-list envelope: one
// named array of records plus an optional pagination continuation, e.g.
// {"filings": [...], "next_page_url": "..."}, mirroring compat's
// list_response family (fd.list_response in the Python source).
type ListResponse struct {
	Key         string
	Records     any
	NextPageURL *string
}

// NewListResponse builds one ListResponse envelope, mirroring
// fd.list_response(key, records, next_url).
func NewListResponse(key string, records any, nextURL *string) ListResponse {
	return ListResponse{Key: key, Records: records, NextPageURL: nextURL}
}

// MarshalJSON renders {Key: Records[, "next_page_url": NextPageURL]},
// preserving that key order regardless of Go's map-key sorting.
func (r ListResponse) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	keyJSON, err := json.Marshal(r.Key)
	if err != nil {
		return nil, err
	}
	buf.Write(keyJSON)
	buf.WriteByte(':')
	valueJSON, err := json.Marshal(r.Records)
	if err != nil {
		return nil, err
	}
	buf.Write(valueJSON)
	if r.NextPageURL != nil {
		buf.WriteString(`,"next_page_url":`)
		urlJSON, err := json.Marshal(*r.NextPageURL)
		if err != nil {
			return nil, err
		}
		buf.Write(urlJSON)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// PricesResponse mirrors the Financial Datasets PricesResponse schema
// ("ticker", "prices", "next_page_url"), built by fd.prices_response.
type PricesResponse struct {
	Ticker      *string `json:"ticker,omitempty"`
	Prices      []Price `json:"prices,omitempty"`
	NextPageURL *string `json:"next_page_url,omitempty"`
}

// NewPricesResponse builds one page of prices, mirroring
// fd.prices_response(ticker, records, next_url).
func NewPricesResponse(ticker string, records []Price, nextURL *string) PricesResponse {
	return PricesResponse{Ticker: &ticker, Prices: records, NextPageURL: nextURL}
}

// PriceSnapshotResponse mirrors the Financial Datasets PriceSnapshotResponse
// schema ({"snapshot": {...}}), built by fd.price_snapshot_response.
type PriceSnapshotResponse struct {
	Snapshot PriceSnapshot `json:"snapshot"`
}

// NewPriceSnapshotResponse wraps one PriceSnapshot, mirroring
// fd.price_snapshot_response.
func NewPriceSnapshotResponse(snapshot PriceSnapshot) PriceSnapshotResponse {
	return PriceSnapshotResponse{Snapshot: snapshot}
}

// FinancialMetricSnapshotResponse mirrors the Financial Datasets
// FinancialMetricSnapshotResponse schema ({"snapshot": {...}}), built by
// fd.metric_snapshot_record.
type FinancialMetricSnapshotResponse struct {
	Snapshot FinancialMetricSnapshot `json:"snapshot"`
}

// NewFinancialMetricSnapshotResponse wraps one FinancialMetricSnapshot,
// mirroring fd.metric_snapshot_record's {"snapshot": record} wrapping.
func NewFinancialMetricSnapshotResponse(snapshot FinancialMetricSnapshot) FinancialMetricSnapshotResponse {
	return FinancialMetricSnapshotResponse{Snapshot: snapshot}
}

// FinancialsSearchResponse mirrors the Financial Datasets
// FinancialsSearchResponse schema ({"search_results": [...]}), used by
// screen_stocks. The Financial Datasets contract leaves each row's shape
// unreferenced (item_ref is null), so SearchResults is typed `any` here;
// callers (e.g. providers.SearchResult) supply their own row shape.
type FinancialsSearchResponse struct {
	SearchResults any `json:"search_results"`
}

// NewFinancialsSearchResponse wraps one page of search_results, mirroring
// service.screen_stocks's list_response("search_results", records, None)
// call (screen_stocks never paginates: there is no next_page_url).
func NewFinancialsSearchResponse(records any) FinancialsSearchResponse {
	return FinancialsSearchResponse{SearchResults: records}
}
