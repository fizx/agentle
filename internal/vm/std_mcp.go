package vm

import (
	"fmt"

	"go.starlark.net/starlark"
)

// groupMCP: Model Context Protocol client. Requires a granted "mcp" tool config
// (the bound MCP server). Tools can be called directly (mcp_call) or handed to
// the LLM for tool use via llm(messages, tools=mcp_list_tools()).
var groupMCP = []Builtin{
	{Name: "mcp_list_tools", Group: "mcp", Doc: "mcp_list_tools() -> [tool]: the granted MCP server's tools (usable as llm tools=)", Fn: bMCPListTools},
	{Name: "mcp_call", Group: "mcp", Doc: "mcp_call(tool, args={}) -> {text,is_error}: invoke an MCP tool", Fn: bMCPCall},
}

func bMCPListTools(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("mcp_list_tools", args, kwargs); err != nil {
		return nil, err
	}
	return callCap(t, "mcp", "list_tools", map[string]any{}, true, false)
}

func bMCPCall(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var tool string
	var argsVal starlark.Value
	if err := starlark.UnpackArgs("mcp_call", args, kwargs, "tool", &tool, "args?", &argsVal); err != nil {
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
	return callCap(t, "mcp", "call_tool", map[string]any{"tool": tool, "arguments": arguments}, false, false)
}
