package vm

import (
	"errors"
	"fmt"

	"github.com/kylemaxwell/agentle/internal/engine"
	"go.starlark.net/starlark"
)

// groupMCP: Model Context Protocol client. Requires a granted "mcp" tool config
// (the bound MCP server). Tools can be called directly (mcp_call) or handed to
// the LLM for tool use via llm(messages, tools=mcp_list_tools()).
var groupMCP = []Builtin{
	{Name: "mcp_list_tools", Group: "mcp", Doc: "mcp_list_tools() -> [tool]: union of granted MCP servers' tools (empty if none; each tagged with its server)", Fn: bMCPListTools},
	{Name: "mcp_call", Group: "mcp", Doc: "mcp_call(tool, args={}, server='') -> {text,is_error}: invoke an MCP tool (server required only when several are granted)", Fn: bMCPCall},
}

func bMCPListTools(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("mcp_list_tools", args, kwargs); err != nil {
		return nil, err
	}
	res, err := callCap(t, "mcp", "list_tools", map[string]any{}, true, false)
	if err != nil {
		// Listing tools is always allowed; with no MCP server granted it's simply
		// empty rather than an error.
		if errors.Is(err, engine.ErrNotGranted) {
			return starlark.NewList(nil), nil
		}
		return nil, err
	}
	return res, nil
}

func bMCPCall(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var tool, server string
	var argsVal starlark.Value
	if err := starlark.UnpackArgs("mcp_call", args, kwargs, "tool", &tool, "args?", &argsVal, "server?", &server); err != nil {
		return nil, err
	}
	arguments := map[string]any{}
	if argsVal != nil && argsVal != starlark.None {
		gv, err := starlarkToGo(argsVal)
		if err != nil {
			return nil, err
		}
		m, ok := gv.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("mcp_call: args must be a dict")
		}
		arguments = m
	}
	// Tool calls may have side effects: non-idempotent (write-ahead intent + a
	// stable idempotency key so retries/replays don't double-fire).
	return callCap(t, "mcp", "call_tool", map[string]any{"tool": tool, "arguments": arguments, "server": server}, false, false)
}
