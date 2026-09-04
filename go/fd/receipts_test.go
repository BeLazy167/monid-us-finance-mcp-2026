package fd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/belazy/monid-finance/monid"
)

func testRun(provider, endpoint string) *monid.Run {
	return &monid.Run{
		Provider:           provider,
		Endpoint:           endpoint,
		RunID:              "run-1",
		Status:             "COMPLETED",
		Output:             json.RawMessage(`{"ok":true}`),
		ProviderHTTPStatus: 200,
		Cost:               &monid.Money{Value: 0.0006, Currency: "USD"},
		CreatedAt:          "2026-09-04T00:00:00Z",
		CompletedAt:        "2026-09-04T00:00:02Z",
	}
}

// TestReceiptsLedger_RecordsSuccessAndFailure ports
// tests/test_compat_receipts.py::test_ledger_records_success_and_failure.
func TestReceiptsLedger_RecordsSuccessAndFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	ledger := NewReceiptsLedger(path)

	run := testRun("defillama", "/equities/v1/summary")
	if err := ledger.RecordSuccess("get_stock_price", run, nil, map[string]any{"ticker": "AAPL"}); err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	failedRun := testRun("nasdaq", "/get_stock_earnings")
	failedRun.ProviderHTTPStatus = 429
	httpErr := &monid.RunError{
		Kind: monid.ErrProviderHTTP, Message: "provider returned HTTP 429",
		Run: failedRun, Provider: "nasdaq", Endpoint: "/get_stock_earnings",
	}
	if err := ledger.RecordFailure("get_earnings", "nasdaq", "/get_stock_earnings", httpErr, nil, map[string]any{"symbol": "AAPL"}); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}

	timeoutErr := &monid.RunError{
		Kind: monid.ErrTimeout, Message: "deadline exceeded",
		Provider: "context.dev", Endpoint: "/news/search",
	}
	if err := ledger.RecordFailure("get_news", "context.dev", "/news/search", timeoutErr, map[string]any{"limit": float64(5)}, nil); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	var success, httpFailure, timeoutFailure map[string]any
	for i, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		switch i {
		case 0:
			success = row
		case 1:
			httpFailure = row
		case 2:
			timeoutFailure = row
		}
	}

	if success["tool"] != "get_stock_price" {
		t.Fatalf("got tool=%v, want get_stock_price", success["tool"])
	}
	if success["run_id"] != "run-1" {
		t.Fatalf("got run_id=%v, want run-1", success["run_id"])
	}
	cost, ok := success["measured_cost"].(map[string]any)
	if !ok || cost["currency"] != "USD" {
		t.Fatalf("got measured_cost=%v, want value=0.0006 currency=USD", success["measured_cost"])
	}
	if success["lifecycle_status"] != "COMPLETED" {
		t.Fatalf("got lifecycle_status=%v, want COMPLETED", success["lifecycle_status"])
	}
	if _, hasError := success["error"]; hasError {
		t.Fatal("success row must not have an error field")
	}

	if int(httpFailure["provider_http_status"].(float64)) != 429 {
		t.Fatalf("got provider_http_status=%v, want 429", httpFailure["provider_http_status"])
	}
	if !strings.Contains(httpFailure["error"].(string), "ProviderHTTP") {
		t.Fatalf("got error=%v, want it to mention ProviderHTTP", httpFailure["error"])
	}

	if _, hasRunID := timeoutFailure["run_id"]; hasRunID {
		t.Fatal("a run-less failure must omit run_id")
	}
	if !strings.Contains(timeoutFailure["error"].(string), "Timeout") {
		t.Fatalf("got error=%v, want it to mention Timeout", timeoutFailure["error"])
	}

	summary, err := SummarizeLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Calls != 3 {
		t.Fatalf("got calls=%d, want 3", summary.Calls)
	}
	if summary.Failures != 2 {
		t.Fatalf("got failures=%d, want 2", summary.Failures)
	}
	if diff := summary.TotalUSDCost - 0.0012; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("got total_usd_cost=%v, want 0.0012", summary.TotalUSDCost)
	}
	earnings, ok := summary.Tools["get_earnings"]
	if !ok {
		t.Fatal("expected a get_earnings tool summary")
	}
	if earnings.Failures != 1 {
		t.Fatalf("got get_earnings.failures=%d, want 1", earnings.Failures)
	}
	if diff := earnings.USDCost - 0.0006; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("got get_earnings.usd_cost=%v, want 0.0006", earnings.USDCost)
	}
	price, ok := summary.Tools["get_stock_price"]
	if !ok {
		t.Fatal("expected a get_stock_price tool summary")
	}
	if diff := price.USDCost - 0.0006; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("got get_stock_price.usd_cost=%v, want 0.0006", price.USDCost)
	}
}

func TestSummarizeLedger_MissingFileIsZero(t *testing.T) {
	summary, err := SummarizeLedger(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Calls != 0 || summary.Failures != 0 || summary.TotalUSDCost != 0 {
		t.Fatalf("got %+v, want all zero", summary)
	}
}

func TestReceiptsLedger_RecordFailureWithoutRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.jsonl")
	ledger := NewReceiptsLedger(path)
	defer ledger.Close()

	plainErr := errors.New("boom")
	if err := ledger.RecordFailure("get_news", "context.dev", "/news/search", plainErr, nil, nil); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	if _, hasRunID := row["run_id"]; hasRunID {
		t.Fatal("a run-less failure must omit run_id")
	}
	if !strings.Contains(row["error"].(string), "boom") {
		t.Fatalf("got error=%v, want it to mention boom", row["error"])
	}
}

func TestReceiptsLedger_SpentUSDAccumulates(t *testing.T) {
	dir := t.TempDir()
	ledger := NewReceiptsLedger(filepath.Join(dir, "ledger.jsonl"))
	defer ledger.Close()

	if err := ledger.RecordSuccess("t1", testRun("defillama", "/e1"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordSuccess("t2", testRun("defillama", "/e2"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if diff := ledger.SpentUSD() - 0.0012; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("got spent=%v, want 0.0012", ledger.SpentUSD())
	}
}
