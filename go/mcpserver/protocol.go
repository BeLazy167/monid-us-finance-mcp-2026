// Package mcpserver speaks the MCP streamable-HTTP protocol with the exact
// tool surface of the Financial Datasets MCP server.
//
// The 27 tool definitions are embedded verbatim from a live capture of that
// server (tool_schemas.json), so names, parameter names, ordering, defaults,
// enums, required flags and descriptions match by construction rather than by
// hand-copying.
package mcpserver

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

//go:embed tool_schemas.json
var toolSchemasJSON []byte

const (
	protocolVersion = "2025-03-26"
	serverName      = "Monid Finance MCP"
	serverVersion   = "1.0.0"
)

// Tool is one MCP tool definition as advertised by tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Dispatcher executes a tool call. Implementations return the FD-shaped
// result value: a bare array for list tools, a bare object for snapshot
// tools, or the FD error object. The caller's Monid API key travels in ctx.
type Dispatcher interface {
	Call(r *http.Request, name string, args map[string]any) (any, error)
}

// Server answers MCP JSON-RPC requests over HTTP.
type Server struct {
	tools      []Tool
	byName     map[string]Tool
	dispatcher Dispatcher
}

// New loads the embedded tool surface and binds it to a dispatcher.
func New(dispatcher Dispatcher) (*Server, error) {
	var tools []Tool
	if err := json.Unmarshal(toolSchemasJSON, &tools); err != nil {
		return nil, fmt.Errorf("mcpserver: embedded tool schemas: %w", err)
	}
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	return &Server{tools: tools, byName: byName, dispatcher: dispatcher}, nil
}

// Tools exposes the advertised tool set (used by tests and the docs route).
func (s *Server) Tools() []Tool { return s.tools }

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// ServeHTTP handles one MCP request. POST carries JSON-RPC; GET is refused
// because this server does not offer a standalone SSE stream.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req rpcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}

	// Notifications carry no id and expect no response body.
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": serverVersion},
		}})
	case "ping":
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.tools}})
	case "tools/call":
		s.handleCall(w, r, req)
	default:
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
	}
}

// handleCall runs one tool and wraps its FD-shaped value as MCP text content,
// matching the upstream server (the text is the bare JSON result).
func (s *Server) handleCall(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}})
		return
	}
	if _, ok := s.byName[params.Name]; !ok {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}})
		return
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}

	value, err := s.dispatcher.Call(r, params.Name, params.Arguments)
	if err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32603, Message: err.Error()}})
		return
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32603, Message: "could not encode tool result"}})
		return
	}
	writeRPC(w, r, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(encoded)}},
	}})
}

// writeRPC answers with SSE when the client accepts it (the transport used by
// Claude, Cursor and the reference clients), otherwise plain JSON.
func writeRPC(w http.ResponseWriter, r *http.Request, resp rpcResponse) {
	body, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "encoding error", http.StatusInternalServerError)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", body)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
