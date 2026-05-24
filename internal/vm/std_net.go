package vm

import (
	"errors"

	"github.com/kylemaxwell/agentle/internal/engine"
	"go.starlark.net/starlark"
)

// groupNet: network capabilities. These require a granted tool config (http/llm);
// an ungranted call fails with "capability not granted".
var groupNet = []Builtin{
	{Name: "http_get", Group: "net", Doc: "http_get(url, headers={}) -> {status,body,headers}", Fn: bHTTPGet},
	{Name: "http_post", Group: "net", Doc: "http_post(url, body='', headers={}) -> {status,body,headers}", Fn: bHTTPPost},
	{Name: "llm", Group: "net", Doc: "llm(messages, model='', temperature=0, tools=[]) -> {role,content,tool_calls}", Fn: bLLM},
}

func bHTTPGet(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var url string
	var headers *starlark.Dict
	if err := starlark.UnpackArgs("http_get", args, kwargs, "url", &url, "headers?", &headers); err != nil {
		return nil, err
	}
	h, err := dictToStringMap(headers)
	if err != nil {
		return nil, err
	}
	return callCap(t, "http", "get", map[string]any{"url": url, "headers": h}, true, false)
}

func bHTTPPost(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var url, body string
	var headers *starlark.Dict
	if err := starlark.UnpackArgs("http_post", args, kwargs, "url", &url, "body?", &body, "headers?", &headers); err != nil {
		return nil, err
	}
	h, err := dictToStringMap(headers)
	if err != nil {
		return nil, err
	}
	return callCap(t, "http", "post", map[string]any{"url": url, "body": body, "headers": h}, false, false)
}

func bLLM(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var messages starlark.Value
	var model string
	var temperature float64
	var tools starlark.Value
	if err := starlark.UnpackArgs("llm", args, kwargs, "messages", &messages, "model?", &model, "temperature?", &temperature, "tools?", &tools); err != nil {
		return nil, err
	}
	msgs, err := starlarkToGo(messages)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"messages": msgs, "model": model, "temperature": temperature}
	if tools != nil && tools != starlark.None {
		tv, err := starlarkToGo(tools)
		if err != nil {
			return nil, err
		}
		payload["tools"] = tv
	} else if def, err := defaultMCPTools(t); err != nil {
		return nil, err
	} else if def != nil {
		// No explicit tools: default to the tools implied by granted MCP servers.
		payload["tools"] = def
	}
	return callCap(t, "llm", "chat", payload, true, false)
}

// defaultMCPTools returns the granted MCP servers' tools as the LLM's default tool
// list, or nil when no MCP server is granted (so llm-only scripts add no RPC).
func defaultMCPTools(t *starlark.Thread) (any, error) {
	res, err := callCap(t, "mcp", "list_tools", map[string]any{}, true, false)
	if err != nil {
		if errors.Is(err, engine.ErrNotGranted) {
			return nil, nil
		}
		return nil, err
	}
	return starlarkToGo(res)
}
