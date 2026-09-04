// Package monid is the HTTP transport for the Monid API.
//
// Every call carries the caller's own Monid API key, so usage bills the
// caller's wallet. Keys are used per request and never logged or persisted.
package monid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.monid.ai/v1"

// Money is a measured cost in USD.
type Money struct {
	Value    float64
	Currency string
}

// Run is one completed Monid endpoint execution.
type Run struct {
	Provider           string
	Endpoint           string
	RunID              string
	Status             string
	Output             json.RawMessage
	ProviderHTTPStatus int
	Cost               *Money
	CreatedAt          string
	CompletedAt        string
}

// Error kinds returned by the transport, mapped to Financial Datasets error
// codes by the service layer.
var (
	ErrUnauthorized = errors.New("monid: invalid or unauthorized API key")
	ErrBlocked      = errors.New("monid: run blocked by a workspace control")
	ErrProviderHTTP = errors.New("monid: provider returned an error status")
	ErrTimeout      = errors.New("monid: run timed out")
	ErrSchema       = errors.New("monid: unexpected response shape")
	ErrNotAllowed   = errors.New("monid: endpoint is not in the discovery allowlist")
)

// RunError carries the failing run alongside the error kind so the service
// layer can still record a receipt for it.
type RunError struct {
	Kind     error
	Message  string
	Run      *Run
	Provider string
	Endpoint string
}

func (e *RunError) Error() string { return e.Message }
func (e *RunError) Unwrap() error { return e.Kind }

// Input is the composite endpoint input: body + query + path parameters.
type Input struct {
	Body        map[string]any `json:"body,omitempty"`
	QueryParams map[string]any `json:"queryParams,omitempty"`
	PathParams  map[string]any `json:"pathParams,omitempty"`
}

func (i Input) empty() bool {
	return len(i.Body) == 0 && len(i.QueryParams) == 0 && len(i.PathParams) == 0
}

// Client executes Monid endpoints over HTTP.
type Client struct {
	APIKey    string
	BaseURL   string
	HTTP      *http.Client
	Allowlist Allowlist
	// ArtifactTimeout bounds one artifact download; zero means 60s.
	ArtifactTimeout time.Duration
	// slots bounds concurrent runs so one request cannot exhaust the pool.
	slots chan struct{}
}

// NewClient returns a client bound to one caller's API key. The http client is
// shared so connections pool across callers.
func NewClient(apiKey string, httpClient *http.Client, allowlist Allowlist, maxConcurrent int) *Client {
	if maxConcurrent < 1 {
		maxConcurrent = 8
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 180 * time.Second}
	}
	return &Client{
		APIKey:    apiKey,
		BaseURL:   DefaultBaseURL,
		HTTP:      httpClient,
		Allowlist: allowlist,
		slots:     make(chan struct{}, maxConcurrent),
	}
}

// Run executes one endpoint and returns the parsed run.
func (c *Client) Run(ctx context.Context, provider, endpoint string, input Input) (*Run, error) {
	if c.Allowlist != nil && !c.Allowlist.Permits(provider, endpoint) {
		return nil, &RunError{
			Kind:     ErrNotAllowed,
			Message:  fmt.Sprintf("endpoint %s %s is not in the validated discovery allowlist", provider, endpoint),
			Provider: provider, Endpoint: endpoint,
		}
	}

	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, &RunError{Kind: ErrTimeout, Message: "monid: timed out waiting for a run slot", Provider: provider, Endpoint: endpoint}
	}

	payload := map[string]any{"provider": provider, "endpoint": endpoint}
	if !input.empty() {
		payload["input"] = input
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not encode request", Provider: provider, Endpoint: endpoint}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/run", bytes.NewReader(body))
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not build request", Provider: provider, Endpoint: endpoint}
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &RunError{Kind: ErrTimeout, Message: "monid: run timed out", Provider: provider, Endpoint: endpoint}
		}
		// The key must never reach an error string.
		return nil, &RunError{Kind: ErrSchema, Message: "monid: request failed", Provider: provider, Endpoint: endpoint}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not read response", Provider: provider, Endpoint: endpoint}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &RunError{Kind: ErrUnauthorized, Message: ErrUnauthorized.Error(), Provider: provider, Endpoint: endpoint}
	case resp.StatusCode == http.StatusPaymentRequired:
		return nil, &RunError{Kind: ErrBlocked, Message: "monid: wallet balance is insufficient for this run", Provider: provider, Endpoint: endpoint}
	case resp.StatusCode >= 500:
		return nil, &RunError{Kind: ErrProviderHTTP, Message: fmt.Sprintf("monid: API returned HTTP %d", resp.StatusCode), Provider: provider, Endpoint: endpoint}
	}

	run, err := parseRun(raw, provider, endpoint)
	if err != nil {
		return nil, err
	}
	// A run may still be in flight; wait for it to settle before judging it.
	run, err = c.awaitRun(ctx, run, provider, endpoint)
	if err != nil {
		return nil, err
	}
	if run.Status == "BLOCKED" {
		return nil, &RunError{Kind: ErrBlocked, Message: "monid: run was blocked by a workspace control", Run: run, Provider: provider, Endpoint: endpoint}
	}
	if run.Status != "COMPLETED" {
		return nil, &RunError{Kind: ErrProviderHTTP, Message: fmt.Sprintf("monid: run ended with status %s", run.Status), Run: run, Provider: provider, Endpoint: endpoint}
	}
	if run.ProviderHTTPStatus >= 400 {
		return nil, &RunError{Kind: ErrProviderHTTP, Message: fmt.Sprintf("provider returned HTTP %d", run.ProviderHTTPStatus), Run: run, Provider: provider, Endpoint: endpoint}
	}

	// Large payloads arrive as a signed artifact link rather than inline JSON,
	// so resolve them before the caller ever sees the output.
	hydrated, err := c.hydrateArtifacts(ctx, run.Output, provider, endpoint)
	if err != nil {
		return nil, err
	}
	run.Output = hydrated
	return run, nil
}
