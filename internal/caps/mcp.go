package caps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/mcp"
)

// MCPConfig is a bound MCP server instance. An empty Endpoint selects an offline
// mock server (demo tools) so MCP scripts are playable with no external server.
type MCPConfig struct {
	Endpoint string // MCP server URL (JSON-RPC 2.0 over HTTP); empty => mock
	APIKey   string // secret; injected as Bearer, never visible to the script
	Timeout  time.Duration
	Client   *http.Client
}

// MCP returns the "mcp" capability executor: a Model Context Protocol client.
//
//   - list_tools                       -> the server's tool catalog
//   - call_tool {tool, arguments}      -> {content, text, is_error}
//
// Tool calls are surfaced to scripts directly (mcp_call) and can also be hosted
// for an LLM as OpenAI function tools (see ToolSpecs / the llm tools= argument).
func MCP(cfg MCPConfig) engine.Executor {
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.Timeout}
	}
	mock := strings.TrimSpace(cfg.Endpoint) == ""
	demo := mcp.NewDemo()

	return engine.ExecutorFunc(func(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
		switch inv.Method {
		case "list_tools":
			if mock {
				return json.Marshal(demo.Tools())
			}
			var res struct {
				Tools json.RawMessage `json:"tools"`
			}
			if err := mcpRPC(ctx, cfg, "tools/list", map[string]any{}, &res); err != nil {
				return nil, err
			}
			return res.Tools, nil

		case "call_tool":
			var a struct {
				Tool      string         `json:"tool"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			if mock {
				text, err := demo.Call(a.Tool, a.Arguments)
				return toolResult(text, err), nil
			}
			var res struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			if err := mcpRPC(ctx, cfg, "tools/call", map[string]any{"name": a.Tool, "arguments": a.Arguments}, &res); err != nil {
				return nil, err
			}
			var text strings.Builder
			for _, c := range res.Content {
				if c.Type == "text" {
					text.WriteString(c.Text)
				}
			}
			if res.IsError {
				return toolResult(text.String(), fmt.Errorf("%s", text.String())), nil
			}
			return toolResult(text.String(), nil), nil

		default:
			return json.RawMessage(`null`), nil
		}
	})
}

// toolResult is the shape returned to the script for a tool call: a flattened
// text field plus an error flag. A tool error is reported in-band (is_error) so
// an LLM agent can read and recover from it rather than aborting the run.
func toolResult(text string, err error) json.RawMessage {
	out := map[string]any{"text": text, "is_error": err != nil}
	if err != nil && text == "" {
		out["text"] = err.Error()
	}
	b, _ := json.Marshal(out)
	return b
}

// mcpRPC performs one JSON-RPC 2.0 call against the configured MCP server and
// decodes result into out.
func mcpRPC(ctx context.Context, cfg MCPConfig, method string, params any, out any) error {
	reqBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("mcp: %s status %d: %s", method, resp.StatusCode, truncate(string(data), 300))
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("mcp: decode %s response: %w", method, err)
	}
	if env.Error != nil {
		return fmt.Errorf("mcp: %s: %s", method, env.Error.Message)
	}
	return json.Unmarshal(env.Result, out)
}
