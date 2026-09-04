package monid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Monid offloads large provider payloads to a signed artifact URL instead of
// inlining them, so a run's output can be a stub of the form
// {"data": {"download_link": "https://sfs.monid.ai/...", "content_type": "application/json"}}.
// Hydration replaces those stubs with the downloaded JSON, so callers always
// see the real payload.

const (
	// artifactHost is the only host an artifact may be fetched from. Pinning
	// it keeps a hostile or malformed payload from turning this client into a
	// request forwarder.
	artifactHost = "sfs.monid.ai"
	// maxArtifactDepth bounds recursion through nested stubs.
	maxArtifactDepth = 8
	// maxArtifactBytes bounds one artifact download.
	maxArtifactBytes = 256 << 20
)

// hydrateArtifacts walks a run output and replaces every artifact stub with
// the JSON it points at.
func (c *Client) hydrateArtifacts(ctx context.Context, raw json.RawMessage, provider, endpoint string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: run output was not valid JSON", Provider: provider, Endpoint: endpoint}
	}
	hydrated, changed, err := c.hydrateValue(ctx, value, provider, endpoint, 0)
	if err != nil {
		return nil, err
	}
	if !changed {
		return raw, nil
	}
	encoded, err := json.Marshal(hydrated)
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not re-encode hydrated output", Provider: provider, Endpoint: endpoint}
	}
	return encoded, nil
}

// hydrateValue reports the hydrated value and whether anything was replaced.
func (c *Client) hydrateValue(ctx context.Context, value any, provider, endpoint string, depth int) (any, bool, error) {
	if depth > maxArtifactDepth {
		return nil, false, &RunError{Kind: ErrSchema, Message: "monid: artifact nesting is too deep", Provider: provider, Endpoint: endpoint}
	}

	switch typed := value.(type) {
	case map[string]any:
		if link, ok := typed["download_link"].(string); ok {
			contentType, _ := typed["content_type"].(string)
			if contentType != "application/json" {
				return nil, false, &RunError{
					Kind:     ErrSchema,
					Message:  fmt.Sprintf("monid: artifact content type %q is not application/json", contentType),
					Provider: provider, Endpoint: endpoint,
				}
			}
			fetched, err := c.fetchArtifact(ctx, link, provider, endpoint)
			if err != nil {
				return nil, false, err
			}
			return fetched, true, nil
		}
		out := make(map[string]any, len(typed))
		anyChanged := false
		for key, child := range typed {
			hydrated, changed, err := c.hydrateValue(ctx, child, provider, endpoint, depth+1)
			if err != nil {
				return nil, false, err
			}
			out[key] = hydrated
			anyChanged = anyChanged || changed
		}
		return out, anyChanged, nil

	case []any:
		out := make([]any, len(typed))
		anyChanged := false
		for i, child := range typed {
			hydrated, changed, err := c.hydrateValue(ctx, child, provider, endpoint, depth+1)
			if err != nil {
				return nil, false, err
			}
			out[i] = hydrated
			anyChanged = anyChanged || changed
		}
		return out, anyChanged, nil

	default:
		return value, false, nil
	}
}

// fetchArtifact downloads one artifact. Redirects are refused so a signed URL
// cannot bounce the request to another host.
func (c *Client) fetchArtifact(ctx context.Context, rawURL, provider, endpoint string) (any, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !allowedArtifactURL(parsed) {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: artifact URL must be https://" + artifactHost, Provider: provider, Endpoint: endpoint}
	}

	timeout := c.ArtifactTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not build the artifact request", Provider: provider, Endpoint: endpoint}
	}

	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if fetchCtx.Err() != nil {
			return nil, &RunError{Kind: ErrTimeout, Message: "monid: artifact download timed out", Provider: provider, Endpoint: endpoint}
		}
		return nil, &RunError{Kind: ErrSchema, Message: "monid: artifact download failed", Provider: provider, Endpoint: endpoint}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &RunError{Kind: ErrSchema, Message: fmt.Sprintf("monid: artifact download returned HTTP %d", resp.StatusCode), Provider: provider, Endpoint: endpoint}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactBytes+1))
	if err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: could not read the artifact", Provider: provider, Endpoint: endpoint}
	}
	if len(body) > maxArtifactBytes {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: artifact exceeded the size limit", Provider: provider, Endpoint: endpoint}
	}

	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, &RunError{Kind: ErrSchema, Message: "monid: artifact was not valid JSON", Provider: provider, Endpoint: endpoint}
	}
	return decoded, nil
}

// artifactHostForTest lets tests exercise the download path against a local
// server. Production always uses artifactHost.
var artifactHostForTest = ""

// allowedArtifactURL pins artifact downloads to Monid's storage host.
func allowedArtifactURL(u *url.URL) bool {
	if artifactHostForTest != "" {
		return u.Host == artifactHostForTest
	}
	return u.Scheme == "https" && u.Hostname() == artifactHost
}
