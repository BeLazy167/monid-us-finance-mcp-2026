package monid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Some Monid endpoints do not finish inside the initial POST. They answer with
// a non-terminal status and expect the caller to poll GET /v1/runs/{runId}
// until the run settles. Anything that skips this step fails on every slower
// provider, so the client polls rather than treating RUNNING as an error.

// nonTerminalStatuses are the run states that mean "not finished yet".
var nonTerminalStatuses = map[string]bool{
	"RUNNING": true, "PENDING": true, "QUEUED": true, "CREATED": true, "IN_PROGRESS": true,
}

const (
	pollInitialDelay = 750 * time.Millisecond
	pollMaxDelay     = 1500 * time.Millisecond
)

// awaitRun polls until the run reaches a terminal state, the context expires,
// or the deadline passes. It returns the last parsed run.
func (c *Client) awaitRun(ctx context.Context, run *Run, provider, endpoint string) (*Run, error) {
	delay := pollInitialDelay
	for nonTerminalStatuses[strings.ToUpper(run.Status)] {
		select {
		case <-ctx.Done():
			return nil, &RunError{
				Kind:    ErrTimeout,
				Message: fmt.Sprintf("monid: run %s did not finish before the deadline", run.RunID),
				Run:     run, Provider: provider, Endpoint: endpoint,
			}
		case <-time.After(delay):
		}

		fetched, err := c.fetchRun(ctx, run.RunID, provider, endpoint)
		if err != nil {
			return nil, err
		}
		run = fetched

		// Back off gently so a slow provider does not become a poll storm.
		if delay < pollMaxDelay {
			delay *= 2
			if delay > pollMaxDelay {
				delay = pollMaxDelay
			}
		}
	}
	return run, nil
}

// fetchRun reads one run by id.
func (c *Client) fetchRun(ctx context.Context, runID, provider, endpoint string) (*Run, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.BaseURL, "/")+"/runs/"+runID, nil)
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not build the run request", Provider: provider, Endpoint: endpoint}
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &RunError{Kind: ErrTimeout, Message: "monid: polling the run timed out", Provider: provider, Endpoint: endpoint}
		}
		return nil, &RunError{Kind: ErrSchema, Message: "monid: polling the run failed", Provider: provider, Endpoint: endpoint}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not read the run response", Provider: provider, Endpoint: endpoint}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &RunError{Kind: ErrUnauthorized, Message: ErrUnauthorized.Error(), Provider: provider, Endpoint: endpoint}
	case resp.StatusCode >= 400:
		return nil, &RunError{Kind: ErrProviderHTTP, Message: fmt.Sprintf("monid: run lookup returned HTTP %d", resp.StatusCode), Provider: provider, Endpoint: endpoint}
	}

	// A polled run reports its cost under "cost" rather than "billing", so
	// decode leniently and let parseRun apply the shared cost rules.
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: run response was not valid JSON", Provider: provider, Endpoint: endpoint}
	}
	return parseRun(raw, provider, endpoint)
}
