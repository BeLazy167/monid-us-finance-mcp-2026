package monid

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// allowAll permits every endpoint, isolating tests from the discovery artifact.
type allowAll struct{}

func (allowAll) Permits(string, string) bool { return true }

// runBody is a minimal successful /run payload.
func runBody(output string) string {
	return runBodyFor("/equities/v1/summary", output)
}

// runBodyFor builds a successful /run payload for a specific endpoint.
func runBodyFor(endpoint, output string) string {
	return `{"runId":"01TEST","provider":"defillama","endpoint":"` + endpoint + `","status":"COMPLETED",` +
		`"output":` + output + `,"providerResponse":{"httpStatus":200},` +
		`"billing":{"reportedCost":{"currency":"USD","value":600,"unit":"MICRO_DOLLAR"}}}`
}

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("monid_live_secret", srv.Client(), allowAll{}, 4)
	c.BaseURL = srv.URL
	return c, srv
}

func TestRunParsesMeasuredCost(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer monid_live_secret" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(runBody(`{"currentPrice":318.27}`)))
	})

	run, err := c.Run(context.Background(), "defillama", "/equities/v1/summary", Input{
		QueryParams: map[string]any{"ticker": "AAPL"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 600 MICRO_DOLLAR is $0.0006; the ledger must never record a guess.
	if run.Cost == nil || run.Cost.Value != 0.0006 {
		t.Fatalf("cost = %+v, want 0.0006", run.Cost)
	}
	if run.Status != "COMPLETED" || run.ProviderHTTPStatus != 200 {
		t.Fatalf("run = %+v", run)
	}
}

// Large payloads arrive as a signed artifact link; the client must download
// and inline them. This regressed once in production traffic, so it is pinned.
func TestRunHydratesArtifactPayload(t *testing.T) {
	var artifactURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/artifact", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"rows":[[1,2,3]]}`))
	})
	mux.HandleFunc("/run", func(w http.ResponseWriter, _ *http.Request) {
		stub := `{"data":{"download_link":"` + artifactURL + `","content_type":"application/json"}}`
		_, _ = w.Write([]byte(runBodyFor("/equities/v1/ohlcv", stub)))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	artifactURL = srv.URL + "/artifact"

	c := NewClient("monid_live_secret", srv.Client(), allowAll{}, 4)
	c.BaseURL = srv.URL
	// The production client pins artifacts to sfs.monid.ai; point that guard at
	// the test server so the download path itself is exercised.
	origHost := artifactHostForTest
	artifactHostForTest = strings.TrimPrefix(srv.URL, "http://")
	defer func() { artifactHostForTest = origHost }()

	run, err := c.Run(context.Background(), "defillama", "/equities/v1/ohlcv", Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(run.Output, &out); err != nil {
		t.Fatalf("output: %v", err)
	}
	data, ok := out["data"].(map[string]any)
	if !ok {
		t.Fatalf("data was not hydrated: %s", run.Output)
	}
	if _, ok := data["rows"]; !ok {
		t.Fatalf("artifact rows missing: %s", run.Output)
	}
	if _, stillStub := data["download_link"]; stillStub {
		t.Fatal("download_link stub survived hydration")
	}
}

// An artifact must never be fetched from a host other than Monid's.
func TestArtifactHostIsPinned(t *testing.T) {
	c, srv := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		stub := `{"data":{"download_link":"https://attacker.example/x.json","content_type":"application/json"}}`
		_, _ = w.Write([]byte(runBodyFor("/equities/v1/ohlcv", stub)))
	})
	_ = srv

	_, err := c.Run(context.Background(), "defillama", "/equities/v1/ohlcv", Input{})
	if err == nil || !errors.Is(err, ErrSchema) {
		t.Fatalf("err = %v, want a schema error rejecting the host", err)
	}
}

func TestUnauthorizedNeverLeaksTheKey(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := c.Run(context.Background(), "defillama", "/equities/v1/summary", Input{})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "monid_live_secret") {
		t.Fatal("the API key leaked into an error message")
	}
}

func TestProviderErrorStatusIsClassified(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"runId":"01TEST","status":"COMPLETED","output":{},"providerResponse":{"httpStatus":429},` +
			`"billing":{"reportedCost":{"currency":"USD","value":0,"unit":"MICRO_DOLLAR"}}}`))
	})
	_, err := c.Run(context.Background(), "defillama", "/equities/v1/summary", Input{})
	if !errors.Is(err, ErrProviderHTTP) {
		t.Fatalf("err = %v, want ErrProviderHTTP", err)
	}
}

// A 429 from the Monid API itself must surface as ErrRateLimited (mapped to
// HTTP 429 rate_limited), so a client's retry logic fires instead of
// reading an outage.
func TestMonidRateLimitIsClassified(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":429,"message":"rate limit exceeded"}`))
	})
	_, err := c.Run(context.Background(), "defillama", "/equities/v1/summary", Input{})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

// An endpoint outside the validated discovery artifact must never be called.
func TestAllowlistBlocksBeforeAnyRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()

	c := NewClient("monid_live_secret", srv.Client(), &DiscoveryAllowlist{pairs: map[string]bool{}}, 4)
	c.BaseURL = srv.URL
	_, err := c.Run(context.Background(), "defillama", "/equities/v1/summary", Input{})
	if !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v, want ErrNotAllowed", err)
	}
	if calls != 0 {
		t.Fatalf("blocked endpoint still issued %d request(s)", calls)
	}
}

// A run without a measured cost is a schema error rather than a free receipt.
func TestMissingCostIsRejected(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"runId":"01TEST","status":"COMPLETED","output":{},"providerResponse":{"httpStatus":200}}`))
	})
	_, err := c.Run(context.Background(), "defillama", "/equities/v1/summary", Input{})
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("err = %v, want ErrSchema", err)
	}
}

// A run that does not finish inside the initial POST must be polled to
// completion. Skipping this fails every slower provider, so it is pinned.
func TestRunPollsUntilComplete(t *testing.T) {
	var runCalls, pollCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/run", func(w http.ResponseWriter, _ *http.Request) {
		runCalls++
		_, _ = w.Write([]byte(`{"runId":"01ASYNC","provider":"secform4","endpoint":"/get_13d_filings",` +
			`"status":"RUNNING","price":{"amount":{"value":0.01,"currency":"USD"}}}`))
	})
	mux.HandleFunc("/runs/01ASYNC", func(w http.ResponseWriter, _ *http.Request) {
		pollCalls++
		status := "RUNNING"
		if pollCalls >= 2 {
			status = "COMPLETED"
		}
		_, _ = w.Write([]byte(`{"runId":"01ASYNC","provider":"secform4","endpoint":"/get_13d_filings",` +
			`"status":"` + status + `","output":{"items":[{"type":"13D"}]},"providerResponse":{"httpStatus":200},` +
			`"cost":{"currency":"USD","value":0.01,"unit":"DOLLAR"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient("monid_live_secret", srv.Client(), allowAll{}, 4)
	c.BaseURL = srv.URL

	run, err := c.Run(context.Background(), "secform4", "/get_13d_filings", Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Status != "COMPLETED" {
		t.Fatalf("status = %s, want COMPLETED", run.Status)
	}
	if pollCalls < 2 {
		t.Fatalf("polled %d times, expected to keep polling while RUNNING", pollCalls)
	}
	// A polled run reports its settled cost under "cost", not "billing".
	if run.Cost == nil || run.Cost.Value != 0.01 {
		t.Fatalf("cost = %+v, want 0.01 from the polled run", run.Cost)
	}
	if !strings.Contains(string(run.Output), "13D") {
		t.Fatalf("output not carried through polling: %s", run.Output)
	}
}

// Polling must stop when the caller's context expires.
func TestRunPollingRespectsDeadline(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/run", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"runId":"01SLOW","status":"RUNNING","price":{"amount":{"value":0.01,"currency":"USD"}}}`))
	})
	mux.HandleFunc("/runs/01SLOW", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"runId":"01SLOW","status":"RUNNING","price":{"amount":{"value":0.01,"currency":"USD"}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient("monid_live_secret", srv.Client(), allowAll{}, 4)
	c.BaseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	_, err := c.Run(ctx, "secform4", "/get_13d_filings", Input{})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}
