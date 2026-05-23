// Package mcp is a minimal Model Context Protocol implementation: a JSON-RPC 2.0
// tool server (Streamable-HTTP transport — requests POSTed to one endpoint) and
// the shared tool types. The agentle MCP capability is a client of this protocol;
// this server lets the platform expose tools over MCP and gives the client a real
// peer to talk to in tests and demos. It is intentionally small (no sessions,
// notifications, or resources) — just initialize / tools/list / tools/call.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Tool is an MCP tool definition (name + JSON-Schema input).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolFunc implements a tool: it receives decoded arguments and returns text.
type ToolFunc func(args map[string]any) (string, error)

// Server is a JSON-RPC 2.0 MCP tool server over HTTP.
type Server struct {
	tools    []Tool
	handlers map[string]ToolFunc
}

// New returns an empty server. Register tools with Add.
func New() *Server { return &Server{handlers: map[string]ToolFunc{}} }

// Add registers a tool and its handler.
func (s *Server) Add(t Tool, fn ToolFunc) {
	s.tools = append(s.tools, t)
	s.handlers[t.Name] = fn
}

// Tools returns the registered tool definitions.
func (s *Server) Tools() []Tool { return s.tools }

// Call invokes a registered tool by name.
func (s *Server) Call(name string, args map[string]any) (string, error) {
	fn, ok := s.handlers[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return fn(args)
}

// NewDemo returns a server with a few side-effect-free demo tools, used for the
// offline MCP mock and the examples so the platform is playable with no external
// MCP server configured.
func NewDemo() *Server {
	s := New()
	s.Add(Tool{
		Name:        "echo",
		Description: "Echo back the given text.",
		InputSchema: object(map[string]any{"text": prop("string", "text to echo")}, "text"),
	}, func(a map[string]any) (string, error) {
		return str(a["text"]), nil
	})
	s.Add(Tool{
		Name:        "add",
		Description: "Add two numbers a and b.",
		InputSchema: object(map[string]any{"a": prop("number", "first addend"), "b": prop("number", "second addend")}, "a", "b"),
	}, func(a map[string]any) (string, error) {
		return fmt.Sprintf("%v", num(a["a"])+num(a["b"])), nil
	})
	s.Add(Tool{
		Name:        "upper",
		Description: "Uppercase the given text.",
		InputSchema: object(map[string]any{"text": prop("string", "text to uppercase")}, "text"),
	}, func(a map[string]any) (string, error) {
		return upper(str(a["text"])), nil
	})
	return s
}

// --- JSON-RPC 2.0 over HTTP -------------------------------------------------

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

// ServeHTTP handles a single JSON-RPC request per POST.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "mcp: POST only", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]any{"name": "agentle-mcp", "version": "0.1"},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.tools}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		text, err := s.Call(p.Name, p.Arguments)
		if err != nil {
			resp.Result = map[string]any{"content": []any{textContent(err.Error())}, "isError": true}
		} else {
			resp.Result = map[string]any{"content": []any{textContent(text)}, "isError": false}
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func textContent(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// --- schema/value helpers ---------------------------------------------------

func object(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required}
}
func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
