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

// MCPServer is one bound MCP server instance. An empty Endpoint selects an offline
// mock server (demo tools) so MCP scripts are playable with no external server.
type MCPServer struct {
	Name     string // server name surfaced to scripts (tool["server"])
	Endpoint string // MCP server URL (JSON-RPC 2.0 over HTTP); empty => mock
	APIKey   string // secret; injected as Bearer, never visible to the script
}

// MCPRouter returns the "mcp" capability over zero or more bound MCP servers:
//
//   - list_tools                          -> union of every server's tools, each
//                                            tagged with its "server"
//   - call_tool {tool, arguments, server} -> {content, text, is_error}
//
// Routing: an explicit "server" wins; otherwise, with a single server it routes
// there; with several it requires "server" (the tools from list_tools carry it).
func MCPRouter(servers []MCPServer) engine.Executor {
	client := &http.Client{Timeout: 60 * time.Second}
	clients := make([]*mcpClient, len(servers))
	for i, s := range servers {
		clients[i] = &mcpClient{server: s, http: client, mock: strings.TrimSpace(s.Endpoint) == "", demo: mcp.NewDemo()}
	}
	byName := map[string]*mcpClient{}
	for _, c := range clients {
		byName[c.server.Name] = c
	}

	return engine.ExecutorFunc(func(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
		switch inv.Method {
		case "list_tools":
			all := []map[string]any{}
			for _, c := range clients {
				tools, err := c.listTools(ctx)
				if err != nil {
					continue // best-effort: a down server doesn't break listing
				}
				for _, t := range tools {
					t["server"] = c.server.Name
					all = append(all, t)
				}
			}
			return json.Marshal(all)

		case "call_tool":
			var a struct {
				Tool      string         `json:"tool"`
				Arguments map[string]any `json:"arguments"`
				Server    string         `json:"server"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			c, err := routeServer(clients, byName, a.Server)
			if err != nil {
				return toolResult("", err), nil
			}
			return c.callTool(ctx, a.Tool, a.Arguments)

		default:
			return json.RawMessage(`null`), nil
		}
	})
}

// routeServer picks the target server: explicit name, else the sole server, else
// an error asking the caller to disambiguate.
func routeServer(clients []*mcpClient, byName map[string]*mcpClient, server string) (*mcpClient, error) {
	if server != "" {
		if c, ok := byName[server]; ok {
			return c, nil
		}
		return nil, fmt.Errorf("no MCP server named %q", server)
	}
	switch len(clients) {
	case 0:
		return nil, fmt.Errorf("no MCP servers granted")
	case 1:
		return clients[0], nil
	default:
		return nil, fmt.Errorf("multiple MCP servers granted; pass server=")
	}
}

// mcpClient talks to one MCP server (real JSON-RPC over HTTP, or the offline mock).
type mcpClient struct {
	server MCPServer
	http   *http.Client
	mock   bool
	demo   *mcp.Server
}

func (c *mcpClient) listTools(ctx context.Context) ([]map[string]any, error) {
	if c.mock {
		raw, _ := json.Marshal(c.demo.Tools())
		var out []map[string]any
		_ = json.Unmarshal(raw, &out)
		return out, nil
	}
	var res struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := c.rpc(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (c *mcpClient) callTool(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	if c.mock {
		text, err := c.demo.Call(tool, args)
		return toolResult(text, err), nil
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := c.rpc(ctx, "tools/call", map[string]any{"name": tool, "arguments": args}, &res); err != nil {
		return nil, err
	}
	var text strings.Builder
	for _, ct := range res.Content {
		if ct.Type == "text" {
			text.WriteString(ct.Text)
		}
	}
	if res.IsError {
		return toolResult(text.String(), fmt.Errorf("%s", text.String())), nil
	}
	return toolResult(text.String(), nil), nil
}

// toolResult is the shape returned to the script for a tool call: a flattened text
// field plus an error flag. A tool error is reported in-band (is_error) so an LLM
// agent can read and recover from it rather than aborting the run.
func toolResult(text string, err error) json.RawMessage {
	out := map[string]any{"text": text, "is_error": err != nil}
	if err != nil && text == "" {
		out["text"] = err.Error()
	}
	b, _ := json.Marshal(out)
	return b
}

// rpc performs one JSON-RPC 2.0 call against the server and decodes result into out.
func (c *mcpClient) rpc(ctx context.Context, method string, params any, out any) error {
	reqBody, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.server.Endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.server.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.server.APIKey)
	}
	resp, err := c.http.Do(req)
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
