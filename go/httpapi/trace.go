// Opt-in provenance headers, and the comparison proxy the demo page needs.
//
// A Financial Datasets response body must contain only Financial Datasets
// schema keys, so the Monid route a call took cannot ride inside it. It
// rides in a response header instead, and only when the request asks for
// it, which keeps every default response byte-identical to the contract.
package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// comparePrefix is the mount point for the Financial Datasets comparison
// proxy. It sits under /x/ so it can never be mistaken for one of the 54
// Financial Datasets paths.
const comparePrefix = "/x/compare/fd/"

const (
	// traceRequestHeader opts a single request into provenance headers.
	traceRequestHeader = "X-Monid-Trace"
	// traceResponseHeader carries the JSON array of TraceStep.
	traceResponseHeader = "X-Monid-Trace"
	// traceCostHeader carries the summed measured cost in USD, so a
	// caller can read the price without parsing the route.
	traceCostHeader = "X-Monid-Cost-USD"
	// traceMaxBytes bounds the header. A chain is one to four runs, so
	// this is only a guard against a pathological fan-out producing a
	// header a proxy would reject.
	traceMaxBytes = 6144
)

// wantsTrace reports whether the request opted into provenance headers.
func wantsTrace(r *http.Request) bool {
	v := r.Header.Get(traceRequestHeader)
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// writeTraceHeaders attaches the route and its cost, when asked for. It is
// called before the body is written, since headers cannot follow it.
func writeTraceHeaders(w http.ResponseWriter, r *http.Request, trace []TraceStep) {
	if !wantsTrace(r) || len(trace) == 0 {
		return
	}
	encoded, err := json.Marshal(trace)
	if err != nil || len(encoded) > traceMaxBytes {
		return
	}
	var total float64
	for _, step := range trace {
		if step.CostUSD != nil {
			total += *step.CostUSD
		}
	}
	w.Header().Set(traceResponseHeader, string(encoded))
	w.Header().Set(traceCostHeader, formatUSD(total))
}

// formatUSD renders a measured cost with enough precision to show a
// $0.0006 run without scientific notation.
func formatUSD(v float64) string {
	return strings.TrimRight(strings.TrimRight(fixed6(v), "0"), ".")
}

func fixed6(v float64) string {
	buf := make([]byte, 0, 12)
	return string(appendFixed6(buf, v))
}

func appendFixed6(dst []byte, v float64) []byte {
	if v < 0 {
		dst = append(dst, '-')
		v = -v
	}
	whole := int64(v)
	frac := int64((v-float64(whole))*1e6 + 0.5)
	if frac >= 1e6 {
		whole++
		frac -= 1e6
	}
	dst = appendInt(dst, whole)
	dst = append(dst, '.')
	for div := int64(100000); div >= 1; div /= 10 {
		dst = append(dst, byte('0'+(frac/div)%10))
	}
	return dst
}

func appendInt(dst []byte, v int64) []byte {
	if v == 0 {
		return append(dst, '0')
	}
	var tmp [20]byte
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	return append(dst, tmp[i:]...)
}

// ---- Comparison proxy ----

// fdAPIBase is the only host the comparison proxy will forward to.
const fdAPIBase = "https://api.financialdatasets.ai"

// fdKeyHeader carries the caller's own Financial Datasets key. It is
// deliberately a different name from X-API-KEY so a Monid key can never be
// forwarded to a third party by accident.
const fdKeyHeader = "X-FD-API-KEY"

// fdProxyTimeout bounds one upstream comparison call.
const fdProxyTimeout = 60 * time.Second

// fdProxy forwards one GET to Financial Datasets using the caller's own
// Financial Datasets key, and returns their response verbatim.
//
// It exists for one reason: api.financialdatasets.ai answers browsers
// without an Access-Control-Allow-Origin header, so no page can call it
// directly and show the two APIs side by side. This server can, so the
// comparison page asks it to. The key is read from the request, used for
// the one call, and never stored, logged, or written to the ledger.
func (rt *restAPI) fdProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeFDError(w, http.StatusMethodNotAllowed, "bad_request", "the comparison proxy accepts GET only")
		return
	}
	key := r.Header.Get(fdKeyHeader)
	if key == "" {
		writeFDError(w, http.StatusUnauthorized, "unauthorized", "send your own Financial Datasets key as "+fdKeyHeader)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, comparePrefix)
	if path == "" || strings.Contains(path, "..") {
		writeFDError(w, http.StatusBadRequest, "bad_request", "path is required")
		return
	}
	target := fdAPIBase + "/" + strings.TrimPrefix(path, "/")
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}

	ctx, cancel := context.WithTimeout(r.Context(), fdProxyTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		writeFDError(w, http.StatusBadRequest, "bad_request", "could not build the upstream request")
		return
	}
	req.Header.Set("X-API-KEY", key)
	req.Header.Set("Accept", "application/json")

	started := time.Now()
	resp, err := rt.compareClient().Do(req)
	if err != nil {
		writeFDError(w, http.StatusBadGateway, "upstream_error", "Financial Datasets did not answer")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		writeFDError(w, http.StatusBadGateway, "upstream_error", "could not read the Financial Datasets response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-FD-Elapsed-MS", string(appendInt(nil, time.Since(started).Milliseconds())))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// clientAddr is the rate-limit bucket for an unauthenticated caller: the
// forwarded client address where Fly's edge sets one, else the peer.
func clientAddr(r *http.Request) string {
	if fwd := r.Header.Get("Fly-Client-IP"); fwd != "" {
		return fwd
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// compareClient is the HTTP client the proxy uses. It is separate from the
// Monid transport so a slow comparison can never consume a Monid slot.
func (rt *restAPI) compareClient() *http.Client {
	if rt.compareHTTP != nil {
		return rt.compareHTTP
	}
	return &http.Client{Timeout: fdProxyTimeout}
}
