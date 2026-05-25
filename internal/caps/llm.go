package caps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// LLMConfig is a bound llm tool instance. An empty BaseURL selects the offline
// mock provider so the platform is playable without credentials; once a BaseURL
// is set the real OpenAI-compatible client is used. APIKey is optional — a local
// OpenAI-compatible server (e.g. Ollama) needs no key, while hosted providers do.
type LLMConfig struct {
	BaseURL string // OpenAI-compatible base, e.g. https://api.openai.com/v1
	APIKey  string // secret; injected as Bearer, never visible to the script
	Model   string // default model when the script omits one
	Timeout time.Duration
	Client  *http.Client // optional; defaults to a plain client
}

// LLM returns the "llm" capability executor (OpenAI chat-completions format).
//
// Tool use: when the script passes tools= (OpenAI function specs, or MCP tool
// definitions which are normalized automatically), the model may respond with
// tool_calls. They are returned in a Starlark-friendly shape — a list of
// {id, name, arguments} with arguments decoded to a dict — so the script can
// execute each (e.g. via mcp_call) and append a {"role":"tool", ...} message
// before calling llm again. The capability translates this shape to/from the
// wire format, so it stays compatible with real OpenAI-style servers.
func LLM(cfg LLMConfig) engine.Executor {
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.Timeout}
	}
	mock := cfg.BaseURL == ""
	return engine.ExecutorFunc(func(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
		var a struct {
			Messages    []map[string]any `json:"messages"`
			Model       string           `json:"model"`
			Temperature *float64         `json:"temperature"` // nil = let the provider default (some models reject non-default)
			Tools       []map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(inv.Args, &a); err != nil {
			return nil, err
		}
		tools := normalizeTools(a.Tools)
		model := a.Model
		if model == "" {
			model = cfg.Model
		}
		if mock {
			return mockChat(a.Messages, tools, model)
		}
		body := map[string]any{
			"model":    model,
			"messages": toWireMessages(a.Messages),
		}
		if a.Temperature != nil {
			body["temperature"] = *a.Temperature
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
		reqBody, _ := json.Marshal(body)
		url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}

		resp, err := cfg.Client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("llm: upstream status %d: %s", resp.StatusCode, truncate(string(data), 300))
		}
		var parsed struct {
			Model   string `json:"model"`
			Choices []struct {
				Message struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("llm: decode response: %w", err)
		}
		if len(parsed.Choices) == 0 {
			return nil, fmt.Errorf("llm: empty response")
		}
		usedModel := parsed.Model
		if usedModel == "" {
			usedModel = model
		}
		msg := parsed.Choices[0].Message
		// model + usage travel in the memoized result so cost can be derived later
		// (out of the VM) without re-calling the provider.
		out := map[string]any{"role": msg.Role, "content": msg.Content, "usage": parsed.Usage, "model": usedModel}
		if len(msg.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
				if args == nil {
					args = map[string]any{}
				}
				calls = append(calls, map[string]any{"id": tc.ID, "name": tc.Function.Name, "arguments": args})
			}
			out["tool_calls"] = calls
		}
		return json.Marshal(out)
	})
}

// normalizeTools accepts either OpenAI function specs ({type:"function",...}) or
// MCP tool definitions ({name, description, inputSchema}) and returns OpenAI
// function specs.
func normalizeTools(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if t["type"] == "function" {
			out = append(out, t)
			continue
		}
		name, _ := t["name"].(string)
		if name == "" {
			continue
		}
		fn := map[string]any{"name": name}
		if d, ok := t["description"]; ok {
			fn["description"] = d
		}
		if s, ok := t["inputSchema"]; ok {
			fn["parameters"] = s
		} else {
			fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

// toWireMessages translates the script's Starlark-friendly assistant tool_calls
// ([{id,name,arguments-dict}]) back into OpenAI wire form (arguments as a JSON
// string). Other messages pass through unchanged.
func toWireMessages(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		raw, ok := m["tool_calls"]
		if !ok || raw == nil {
			out = append(out, m)
			continue
		}
		list, ok := raw.([]any)
		if !ok {
			out = append(out, m)
			continue
		}
		wire := make([]map[string]any, 0, len(list))
		for _, c := range list {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			argStr := "{}"
			if b, err := json.Marshal(cm["arguments"]); err == nil {
				argStr = string(b)
			}
			wire = append(wire, map[string]any{
				"id":   cm["id"],
				"type": "function",
				"function": map[string]any{
					"name":      cm["name"],
					"arguments": argStr,
				},
			})
		}
		nm := map[string]any{}
		for k, v := range m {
			nm[k] = v
		}
		nm["tool_calls"] = wire
		out = append(out, nm)
	}
	return out
}

var intRe = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

// mockChat is the offline provider. With tools present it demonstrates a full
// tool-use loop deterministically: it requests the first tool (filling arguments
// from the user's text by a small heuristic), then, once it sees the tool result,
// returns a final answer echoing it. It reports estimated token usage (priced at
// $0 — the "mock" model has no price) so the spend UI shows counts offline.
func mockChat(messages []map[string]any, tools []map[string]any, model string) (json.RawMessage, error) {
	lastRole, lastContent, lastUser := scanMessages(messages)
	if model == "" {
		model = "mock"
	}
	reply := func(content string, toolCalls []map[string]any) (json.RawMessage, error) {
		out := map[string]any{
			"role": "assistant", "content": content, "model": model,
			"usage": mockUsage(messages, content),
		}
		if toolCalls != nil {
			out["tool_calls"] = toolCalls
		}
		return json.Marshal(out)
	}

	if len(tools) > 0 && lastRole != "tool" {
		name := pickTool(tools, lastUser)
		return reply("", []map[string]any{{"id": "call_1", "name": name, "arguments": guessArgs(name, lastUser)}})
	}
	if lastRole == "tool" {
		return reply("[mock] result: "+lastContent, nil)
	}
	content := "[mock] " + truncate(lastUser, 200)
	if lastUser == "" {
		content = "[mock] (no user content)"
	}
	return reply(content, nil)
}

// mockUsage estimates token counts (~4 chars/token) so token tracking is visible
// in the offline mock; the "mock" model is unpriced, so cost stays $0.
func mockUsage(messages []map[string]any, reply string) map[string]any {
	in := 0
	for _, m := range messages {
		if c, ok := m["content"].(string); ok {
			in += len([]rune(c))
		}
	}
	in /= 4
	out := len([]rune(reply)) / 4
	return map[string]any{"prompt_tokens": in, "completion_tokens": out, "total_tokens": in + out, "mock": true}
}

// scanMessages returns the last message's role and content, plus the last user
// message content (for the mock heuristic).
func scanMessages(messages []map[string]any) (lastRole, lastContent, lastUser string) {
	for _, m := range messages {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		lastRole, lastContent = role, content
		if role == "user" {
			lastUser = content
		}
	}
	return
}

// pickTool chooses which tool the offline mock "decides" to call: a tool whose
// name is mentioned in the user's text, else the first one.
func pickTool(tools []map[string]any, userText string) string {
	lower := strings.ToLower(userText)
	first := ""
	for _, t := range tools {
		fn, _ := t["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		if first == "" {
			first = name
		}
		if strings.Contains(lower, strings.ToLower(name)) {
			return name
		}
	}
	return first
}

// guessArgs fills tool arguments for the offline mock from the user's text: the
// "add" tool gets the first two numbers; text tools get the message.
func guessArgs(name, userText string) map[string]any {
	switch name {
	case "add":
		nums := intRe.FindAllString(userText, 2)
		a, b := 0.0, 0.0
		if len(nums) > 0 {
			fmt.Sscanf(nums[0], "%g", &a)
		}
		if len(nums) > 1 {
			fmt.Sscanf(nums[1], "%g", &b)
		}
		return map[string]any{"a": a, "b": b}
	default:
		return map[string]any{"text": userText}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
