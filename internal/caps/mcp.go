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

	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/mcp"
)

// MCPServer is one bound MCP server instance. Routing precedence: a Plugin (an
// agentle-managed program run per-call in the sandbox) wins; else a non-empty
// Endpoint is a real JSON-RPC server; else the offline mock (demo tools), so MCP
// scripts are playable with no external server.
type MCPServer struct {
	Name     string      // server name surfaced to scripts (tool["server"])
	Endpoint string      // MCP server URL (JSON-RPC 2.0 over HTTP); empty => mock
	APIKey   string      // secret; injected as Bearer, never visible to the script
	Plugin   *PluginSpec // set => tools are provided by a sandboxed subprocess plugin
}

// PluginSpec describes an agentle-managed plugin that provides MCP tools. A
// script plugin is run, per call, in the sandbox: argv[1]="list" prints the tool
// catalog; "call" with the tool name + args-JSON prints the result. A native
// plugin (Native != nil) is Go code dispatched in-process instead.
type PluginSpec struct {
	Pool    engine.SandboxPool
	Runtime string // python | node | bash
	Source  string

	Native NativePlugin // set => an in-process Go plugin; Pool/Runtime/Source unused
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
		clients[i] = &mcpClient{server: s, http: client, mock: s.Plugin == nil && strings.TrimSpace(s.Endpoint) == "", demo: mcp.NewDemo()}
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
	if c.server.Plugin != nil {
		if c.server.Plugin.Native != nil {
			return c.server.Plugin.Native.Tools(), nil
		}
		out, err := c.server.Plugin.run(ctx, "list", "", nil)
		if err != nil {
			return nil, err
		}
		var tools []map[string]any
		if err := json.Unmarshal(out, &tools); err != nil {
			return nil, fmt.Errorf("plugin %q list: %w (%s)", c.server.Name, err, truncate(string(out), 200))
		}
		return tools, nil
	}
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
	if c.server.Plugin != nil {
		if c.server.Plugin.Native != nil {
			text, err := c.server.Plugin.Native.Call(ctx, tool, args)
			return toolResult(strings.TrimSpace(text), err), nil
		}
		argsJSON, _ := json.Marshal(args)
		out, err := c.server.Plugin.run(ctx, "call", tool, argsJSON)
		if err != nil {
			return toolResult("", err), nil
		}
		return toolResult(strings.TrimSpace(string(out)), nil), nil
	}
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

// run executes the plugin in a fresh sandbox for one operation (per-call model):
// argv = runtime invocation of the source + [subcmd, tool, argsJSON]. Returns
// stdout. The sandbox is released without persisting (plugins are stateless).
func (p *PluginSpec) run(ctx context.Context, subcmd, tool string, argsJSON []byte) ([]byte, error) {
	if p.Pool == nil {
		return nil, fmt.Errorf("plugin: no sandbox pool configured")
	}
	sb, err := p.Pool.Acquire(ctx, engine.ExecutionID("plugin:"+uuid.NewString()), "", nil)
	if err != nil {
		return nil, err
	}
	defer p.Pool.Release(ctx, sb, false)
	res, err := sb.Exec(ctx, engine.Command{Argv: pluginArgv(p.Runtime, p.Source, subcmd, tool, string(argsJSON))})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("plugin exit %d: %s", res.Code, truncate(string(res.Stderr), 200))
	}
	return res.Stdout, nil
}

// pluginArgv builds the sandbox command that runs a plugin's source for a runtime,
// passing [subcmd, tool, argsJSON] as positional arguments to the program.
func pluginArgv(runtime, source, subcmd, tool, args string) []string {
	switch runtime {
	case "node", "javascript":
		return []string{"node", "-e", source, subcmd, tool, args}
	case "bash", "sh":
		return []string{"bash", "-c", source, "plugin", subcmd, tool, args}
	default: // python
		return []string{"python3", "-c", source, subcmd, tool, args}
	}
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
