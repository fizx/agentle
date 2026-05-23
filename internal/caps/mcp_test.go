package caps

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/mcp"
)

type mcpToolResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error"`
}

// exercise runs the standard list+add+error assertions against an executor.
func exercise(t *testing.T, ex engine.Executor) {
	t.Helper()
	ctx := context.Background()

	raw, err := ex.Execute(ctx, engine.Invocation{Capability: "mcp", Method: "list_tools", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("list_tools: %v", err)
	}
	if !strings.Contains(string(raw), `"add"`) || !strings.Contains(string(raw), `"echo"`) {
		t.Fatalf("tools missing add/echo: %s", raw)
	}

	raw, err = ex.Execute(ctx, engine.Invocation{Method: "call_tool", Args: json.RawMessage(`{"tool":"add","arguments":{"a":2,"b":3}}`)})
	if err != nil {
		t.Fatalf("call add: %v", err)
	}
	var r mcpToolResult
	_ = json.Unmarshal(raw, &r)
	if r.IsError || r.Text != "5" {
		t.Fatalf("add result = %+v (raw %s)", r, raw)
	}

	// Unknown tool is reported in-band (is_error), not as a transport error.
	raw, err = ex.Execute(ctx, engine.Invocation{Method: "call_tool", Args: json.RawMessage(`{"tool":"nope","arguments":{}}`)})
	if err != nil {
		t.Fatalf("call unknown: unexpected transport error %v", err)
	}
	_ = json.Unmarshal(raw, &r)
	if !r.IsError {
		t.Fatalf("expected is_error for unknown tool, got %s", raw)
	}
}

func TestMCPMockMode(t *testing.T) {
	exercise(t, MCP(MCPConfig{})) // empty endpoint => offline mock
}

func TestMCPRealServer(t *testing.T) {
	srv := httptest.NewServer(mcp.NewDemo())
	defer srv.Close()
	exercise(t, MCP(MCPConfig{Endpoint: srv.URL}))
}
