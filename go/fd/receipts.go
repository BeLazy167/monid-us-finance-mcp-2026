// This file ports receipts.py: the append-only JSONL receipts ledger that
// records every Monid call attempt (tool/provider/endpoint/run_id/status/
// measured_cost/error) outside of any tool response, plus its summary
// aggregation.
package fd

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/belazy/monid-finance/monid"
)

// MeasuredCost is one receipt's measured billing cost, mirroring
// receipts._cost's {"value": ..., "currency": ...} shape.
type MeasuredCost struct {
	Value    float64 `json:"value"`
	Currency string  `json:"currency"`
}

// receiptWire is the on-disk JSONL row shape, in the exact key order
// receipts.Receipt.to_dict emits (timestamp, tool, provider, endpoint,
// then whichever optional fields are present, then input last).
type receiptWire struct {
	Timestamp          string         `json:"timestamp"`
	Tool               string         `json:"tool"`
	Provider           string         `json:"provider"`
	Endpoint           string         `json:"endpoint"`
	RunID              *string        `json:"run_id,omitempty"`
	LifecycleStatus    *string        `json:"lifecycle_status,omitempty"`
	ProviderHTTPStatus *int           `json:"provider_http_status,omitempty"`
	MeasuredCost       *MeasuredCost  `json:"measured_cost,omitempty"`
	Error              *string        `json:"error,omitempty"`
	Input              map[string]any `json:"input"`
}

func utcNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
}

func moneyToMeasuredCost(m *monid.Money) *MeasuredCost {
	if m == nil {
		return nil
	}
	return &MeasuredCost{Value: m.Value, Currency: m.Currency}
}

// inputSummary builds the receipt's "input" field: query_params and/or
// body are included only when the caller passed a non-nil map, mirroring
// receipts._input_summary (which checks `is not None`, not emptiness).
func inputSummary(body, queryParams map[string]any) map[string]any {
	summary := map[string]any{}
	if queryParams != nil {
		summary["query_params"] = queryParams
	}
	if body != nil {
		summary["body"] = body
	}
	return summary
}

// errorLabel renders a call failure the way receipts.Receipt.to_dict
// renders Python's f"{type(error).__name__}: {error}", using the
// monid.RunError's sentinel Kind to pick a label. Unrecognized or plain
// errors fall back to "Error: <message>".
func errorLabel(err error) string {
	if err == nil {
		return "Error: unknown error"
	}
	var runErr *monid.RunError
	if errors.As(err, &runErr) {
		kind := "RunError"
		switch {
		case errors.Is(runErr.Kind, monid.ErrTimeout):
			kind = "TimeoutError"
		case errors.Is(runErr.Kind, monid.ErrProviderHTTP):
			kind = "ProviderHTTPError"
		case errors.Is(runErr.Kind, monid.ErrUnauthorized):
			kind = "UnauthorizedError"
		case errors.Is(runErr.Kind, monid.ErrBlocked):
			kind = "BlockedError"
		case errors.Is(runErr.Kind, monid.ErrSchema):
			kind = "SchemaError"
		case errors.Is(runErr.Kind, monid.ErrNotAllowed):
			kind = "NotAllowedError"
		}
		return kind + ": " + runErr.Message
	}
	return "Error: " + err.Error()
}

// ReceiptsLedger is an append-only JSONL ledger of every Monid call, kept
// out of tool responses, mirroring receipts.ReceiptsLedger.
type ReceiptsLedger struct {
	path     string
	mu       sync.Mutex
	file     *os.File
	spentUSD float64
}

// NewReceiptsLedger returns a ledger that appends to path, creating parent
// directories on first write.
func NewReceiptsLedger(path string) *ReceiptsLedger {
	return &ReceiptsLedger{path: path}
}

// Path returns the ledger's backing file path.
func (l *ReceiptsLedger) Path() string { return l.path }

// SpentUSD returns the cumulative measured USD spend recorded so far.
func (l *ReceiptsLedger) SpentUSD() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.spentUSD
}

// RecordSuccess appends one successful-call receipt, mirroring
// ReceiptsLedger.record_success.
func (l *ReceiptsLedger) RecordSuccess(tool string, run *monid.Run, body, queryParams map[string]any) error {
	if run == nil {
		return errors.New("fd: RecordSuccess requires a non-nil run")
	}
	runID := run.RunID
	status := run.Status
	httpStatus := run.ProviderHTTPStatus
	return l.append(receiptWire{
		Timestamp:          utcNowISO(),
		Tool:               tool,
		Provider:           run.Provider,
		Endpoint:           run.Endpoint,
		RunID:              &runID,
		LifecycleStatus:    &status,
		ProviderHTTPStatus: &httpStatus,
		MeasuredCost:       moneyToMeasuredCost(run.Cost),
		Input:              inputSummary(body, queryParams),
	})
}

// RecordFailure appends one failed-call receipt, mirroring
// ReceiptsLedger.record_failure. callErr is typically a *monid.RunError
// (as returned by monid.Client.Run); its Run, if present, supplies run_id/
// lifecycle_status/provider_http_status/measured_cost the same way the
// Python client's exception carries its failed MonidRun.
func (l *ReceiptsLedger) RecordFailure(tool, provider, endpoint string, callErr error, body, queryParams map[string]any) error {
	var runID *string
	var status *string
	var httpStatus *int
	var cost *MeasuredCost
	var runErr *monid.RunError
	if errors.As(callErr, &runErr) && runErr.Run != nil {
		r := runErr.Run
		id, st, hs := r.RunID, r.Status, r.ProviderHTTPStatus
		runID, status, httpStatus = &id, &st, &hs
		cost = moneyToMeasuredCost(r.Cost)
	}
	label := errorLabel(callErr)
	return l.append(receiptWire{
		Timestamp:          utcNowISO(),
		Tool:               tool,
		Provider:           provider,
		Endpoint:           endpoint,
		RunID:              runID,
		LifecycleStatus:    status,
		ProviderHTTPStatus: httpStatus,
		MeasuredCost:       cost,
		Error:              &label,
		Input:              inputSummary(body, queryParams),
	})
}

func (l *ReceiptsLedger) append(rec receiptWire) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	if l.file == nil {
		f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		l.file = f
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if _, err := l.file.Write(raw); err != nil {
		return err
	}
	if rec.MeasuredCost != nil && rec.MeasuredCost.Currency == "USD" {
		l.spentUSD += rec.MeasuredCost.Value
	}
	return nil
}

// Close flushes and closes the ledger's backing file. It is a no-op if
// nothing has been appended yet.
func (l *ReceiptsLedger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// ToolSummary is one tool's aggregated ledger stats, mirroring one entry of
// receipts.summarize_ledger's "tools" mapping.
type ToolSummary struct {
	Calls    int     `json:"calls"`
	Failures int     `json:"failures"`
	USDCost  float64 `json:"usd_cost"`
}

// LedgerSummary is the aggregated cost story for a committed ledger,
// mirroring receipts.summarize_ledger's return shape.
type LedgerSummary struct {
	Calls        int                    `json:"calls"`
	Failures     int                    `json:"failures"`
	TotalUSDCost float64                `json:"total_usd_cost"`
	Tools        map[string]ToolSummary `json:"tools"`
}

// SummarizeLedger aggregates the committed ledger rows at path, mirroring
// receipts.summarize_ledger. A missing file summarizes as zero calls.
func SummarizeLedger(path string) (LedgerSummary, error) {
	tools := map[string]ToolSummary{}
	total := 0.0
	calls := 0
	failures := 0

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LedgerSummary{Tools: tools}, nil
		}
		return LedgerSummary{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		name, _ := row["tool"].(string)
		if name == "" {
			name = "unknown"
		}
		calls++
		_, isFailure := row["error"]
		if isFailure {
			failures++
		}
		entry := tools[name]
		if cost, ok := row["measured_cost"].(map[string]any); ok {
			if currency, _ := cost["currency"].(string); currency == "USD" {
				if value, ok := cost["value"].(float64); ok {
					total += value
					entry.USDCost += value
				}
			}
		}
		entry.Calls++
		if isFailure {
			entry.Failures++
		}
		tools[name] = entry
	}
	return LedgerSummary{
		Calls:        calls,
		Failures:     failures,
		TotalUSDCost: roundTo(total, 6),
		Tools:        tools,
	}, nil
}

func roundTo(v float64, places int) float64 {
	factor := math.Pow(10, float64(places))
	return math.Round(v*factor) / factor
}
