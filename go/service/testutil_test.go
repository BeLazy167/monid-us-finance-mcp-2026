package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
)

// fakeOutcome is one canned /v1/run response (or transport-level failure)
// for one provider+endpoint pair.
type fakeOutcome struct {
	httpStatus         int // the /v1/run endpoint's own HTTP status; 200 for a normal completed run
	output             any // decoded into the run's "output" field when httpStatus == 200
	runStatus          string
	providerHTTPStatus int
	transportErr       error // when set, RoundTrip itself fails (network-level failure)
}

// allowAll permits every provider/endpoint pair - tests exercise business
// logic, not the discovery allowlist.
type allowAll struct{}

func (allowAll) Permits(string, string) bool { return true }

// fakeTransport is an http.RoundTripper stub for monid.Client: no network,
// no paid calls. It records every call (provider, endpoint, body,
// queryParams) and answers from a table of canned outcomes keyed by
// "provider endpoint".
type fakeTransport struct {
	mu       sync.Mutex
	t        *testing.T
	outcomes map[string]fakeOutcome
	calls    []fakeCall
}

type fakeCall struct {
	Provider    string
	Endpoint    string
	Body        map[string]any
	QueryParams map[string]any
}

func newFakeTransport(t *testing.T, outcomes map[string]fakeOutcome) *fakeTransport {
	return &fakeTransport{t: t, outcomes: outcomes}
}

func (f *fakeTransport) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTransport) Calls() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Provider string `json:"provider"`
		Endpoint string `json:"endpoint"`
		Input    struct {
			Body        map[string]any `json:"body"`
			QueryParams map[string]any `json:"queryParams"`
		} `json:"input"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{
		Provider: payload.Provider, Endpoint: payload.Endpoint,
		Body: payload.Input.Body, QueryParams: payload.Input.QueryParams,
	})
	f.mu.Unlock()

	key := payload.Provider + " " + payload.Endpoint
	outcome, ok := f.outcomes[key]
	if !ok {
		if f.t != nil {
			f.t.Fatalf("fakeTransport: no outcome registered for %q", key)
		}
		return nil, errors.New("fakeTransport: no outcome registered for " + key)
	}
	if outcome.transportErr != nil {
		return nil, outcome.transportErr
	}

	status := outcome.httpStatus
	if status == 0 {
		status = http.StatusOK
	}
	runStatus := outcome.runStatus
	if runStatus == "" {
		runStatus = "COMPLETED"
	}
	providerHTTPStatus := outcome.providerHTTPStatus
	if providerHTTPStatus == 0 {
		providerHTTPStatus = 200
	}
	outputRaw, merr := json.Marshal(outcome.output)
	if merr != nil {
		return nil, merr
	}
	wire := map[string]any{
		"runId":            payload.Provider + "-" + payload.Endpoint,
		"provider":         payload.Provider,
		"endpoint":         payload.Endpoint,
		"status":           runStatus,
		"output":           json.RawMessage(outputRaw),
		"providerResponse": map[string]any{"httpStatus": providerHTTPStatus},
		"billing":          map[string]any{"reportedCost": map[string]any{"currency": "USD", "value": 0.001, "unit": "DOLLAR"}},
		"createdAt":        "2026-01-01T00:00:00Z",
		"completedAt":      "2026-01-01T00:00:02Z",
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// newTestService builds a Service wired to a fakeTransport, so no test
// ever reaches the network or spends real money.
func newTestService(t *testing.T, outcomes map[string]fakeOutcome) (*Service, *fakeTransport) {
	t.Helper()
	transport := newFakeTransport(t, outcomes)
	httpClient := &http.Client{Transport: transport}
	svc := New(Config{HTTP: httpClient, Allowlist: allowAll{}, MaxConcurrentRuns: 8})
	return svc, transport
}

func mustCall(t *testing.T, svc *Service, tool string, args map[string]any) Result {
	t.Helper()
	result, err := svc.Call(context.Background(), "test-api-key", tool, args)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", tool, err)
	}
	return result
}

// asRecords type-asserts a Result.Value as []any, the shape every list
// tool returns.
func asRecords(t *testing.T, value any) []any {
	t.Helper()
	records, ok := value.([]any)
	if !ok {
		t.Fatalf("value is not a []any: %#v", value)
	}
	return records
}

// jsonRoundTrip marshals then unmarshals value into a map[string]any, the
// same shape a JSON-over-the-wire caller would see.
func jsonRoundTrip(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}
