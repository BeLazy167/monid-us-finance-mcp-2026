package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeDispatcher returns a fixed FD-shaped value.
type fakeDispatcher struct {
	lastName string
	lastArgs map[string]any
	value    any
}

func (f *fakeDispatcher) Call(_ *http.Request, name string, args map[string]any) (any, error) {
	f.lastName, f.lastArgs = name, args
	return f.value, nil
}

func post(t *testing.T, s *Server, body string, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// decode reads a JSON-RPC response from either transport encoding.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := rec.Body.String()
	if idx := strings.Index(body, "data: "); idx >= 0 {
		body = strings.TrimSpace(body[idx+len("data: "):])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func newServer(t *testing.T, d Dispatcher) *Server {
	t.Helper()
	s, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// The advertised surface must match the captured Financial Datasets schemas
// exactly: same 27 names, same parameter order, same required flags and the
// same descriptions.
func TestToolsListMatchesCapturedFinancialDatasetsSurface(t *testing.T) {
	raw, err := os.ReadFile("../../docs/fd-mcp-tool-schemas.json")
	if err != nil {
		t.Fatalf("read captured schemas: %v", err)
	}
	var captured []Tool
	if err := json.Unmarshal(raw, &captured); err != nil {
		t.Fatalf("parse captured schemas: %v", err)
	}

	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")
	result := decode(t, rec)["result"].(map[string]any)
	advertised := result["tools"].([]any)

	if len(advertised) != len(captured) || len(captured) != 27 {
		t.Fatalf("tool count: advertised %d, captured %d, want 27", len(advertised), len(captured))
	}
	for i, want := range captured {
		got := advertised[i].(map[string]any)
		if got["name"] != want.Name {
			t.Errorf("tool %d name = %v, want %s", i, got["name"], want.Name)
		}
		if got["description"] != want.Description {
			t.Errorf("%s description drifted from the captured surface", want.Name)
		}
		gotSchema, _ := json.Marshal(got["inputSchema"])
		var a, b any
		_ = json.Unmarshal(gotSchema, &a)
		_ = json.Unmarshal(want.InputSchema, &b)
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if string(x) != string(y) {
			t.Errorf("%s inputSchema drifted from the captured surface", want.Name)
		}
	}
}

func TestInitializeReportsServerInfo(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "")
	result := decode(t, rec)["result"].(map[string]any)
	info := result["serverInfo"].(map[string]any)
	if info["name"] != serverName {
		t.Errorf("serverInfo.name = %v, want %s", info["name"], serverName)
	}
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %s", result["protocolVersion"], protocolVersion)
	}
}

// List tools answer with the bare records array as text content, matching the
// upstream server (no wrapper object, no next_page_url).
func TestToolsCallReturnsBareArrayAsTextContent(t *testing.T) {
	records := []any{map[string]any{"ticker": "AAPL", "revenue": 416161000000}}
	d := &fakeDispatcher{value: records}
	s := newServer(t, d)

	rec := post(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_income_statement","arguments":{"ticker":"AAPL"}}}`, "")
	result := decode(t, rec)["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)

	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("tool text was not JSON: %v", err)
	}
	if _, isArray := decoded.([]any); !isArray {
		t.Fatalf("tool result must be a bare array, got %T", decoded)
	}
	if d.lastName != "get_income_statement" || d.lastArgs["ticker"] != "AAPL" {
		t.Errorf("dispatch got %s %v", d.lastName, d.lastArgs)
	}
}

func TestUnknownToolIsRejected(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`, "")
	if _, hasErr := decode(t, rec)["error"]; !hasErr {
		t.Fatal("unknown tool must produce a JSON-RPC error")
	}
}

func TestSSETransportWhenAccepted(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":4,"method":"ping"}`, "application/json, text/event-stream")
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(rec.Body.String(), "event: message") {
		t.Fatalf("SSE framing missing: %q", rec.Body.String())
	}
}

func TestNotificationsGetNoBody(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}
