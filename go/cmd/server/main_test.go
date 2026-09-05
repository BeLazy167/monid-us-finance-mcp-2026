// Package main tests: cmd/server wires httpapi.Caller.Capability's
// non-tool names (go/service/capabilities.go) to Service methods directly,
// deliberately outside Service.Call/toolHandlers' 27 MCP tool names
// (go/mcpserver/tool_schemas.json). This guards that separation: a
// capability name must never collide with a real MCP tool name, since
// Capability and Call are two different, non-interchangeable dispatch
// surfaces on httpapi.Caller.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCapabilityNamesNeverCollideWithMCPToolNames(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mcpserver", "tool_schemas.json"))
	if err != nil {
		t.Fatalf("reading tool_schemas.json: %v", err)
	}
	var schemas []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &schemas); err != nil {
		t.Fatalf("decoding tool_schemas.json: %v", err)
	}
	toolNames := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		toolNames[s.Name] = true
	}
	if len(toolNames) == 0 {
		t.Fatalf("tool_schemas.json yielded no tool names; fixture path likely wrong")
	}
	// list_filing_item_types is the one documented exception (see
	// go/service/capabilities.go's ListFilingItemTypes doc comment): it
	// is both a real MCP tool (reachable via Call, registered in
	// go/service/tools.go's toolHandlers) and, for the REST layer's
	// convenience, a Capability with the identical name and identical
	// underlying handler. Every other capability name must be disjoint
	// from the tool namespace.
	const documentedDualSurface = "list_filing_item_types"
	for name := range capabilityHandlers {
		if name == documentedDualSurface {
			if !toolNames[name] {
				t.Fatalf("%q is expected to also be a real MCP tool name; tool_schemas.json no longer has it - update this exception", name)
			}
			continue
		}
		if toolNames[name] {
			t.Fatalf("capability name %q collides with a real MCP tool name in tool_schemas.json", name)
		}
	}
	// A ratchet, not a specification: a capability missing from this map
	// is unreachable from every surface, and that failure is silent at
	// build time. Bump this deliberately when wiring a new capability;
	// never to make a red test green.
	const wiredCapabilities = 20
	if len(capabilityHandlers) != wiredCapabilities {
		t.Fatalf("capabilityHandlers has %d entries, want %d; if you added a capability, confirm a route reaches it and bump this count",
			len(capabilityHandlers), wiredCapabilities)
	}
}
