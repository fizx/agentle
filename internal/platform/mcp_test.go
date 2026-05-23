package platform

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/examples"
	"github.com/kylemaxwell/agentle/internal/store"
)

// runExample saves an example's source with the given grants and runs it.
func runExample(t *testing.T, s *Service, exID string, grants []store.GrantRef, data string) *store.Execution {
	t.Helper()
	ctx := context.Background()
	ex := examples.Find(exID)
	if ex == nil {
		t.Fatalf("example %q not found", exID)
	}
	if _, err := s.Store.CreateScript(ctx, exID, ex.Title, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.SaveVersion(ctx, exID, ex.Source, "", grants); err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if data != "" {
		raw = json.RawMessage(data)
	}
	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: exID, Kind: "dashboard", Data: raw})
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestMCPDirectExample(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	// Mock MCP server (empty endpoint).
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "mcp-mock", Capability: "mcp", Config: json.RawMessage(`{}`)})

	exe := runExample(t, s, "mcp_direct", []store.GrantRef{{Capability: "mcp", ConfigID: "mcp-mock"}}, "")
	if exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("status=%d err=%s", exe.Status, exe.Error)
	}
	var out struct {
		Tools []string `json:"tools"`
		Sum   string   `json:"sum"`
		Echo  string   `json:"echo"`
	}
	_ = json.Unmarshal(exe.Output, &out)
	if out.Sum != "5" || out.Echo != "hi from starlark" || len(out.Tools) < 3 {
		t.Fatalf("unexpected output: %s", exe.Output)
	}
}

func TestMCPAgentExample(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm", Config: json.RawMessage(`{}`)})
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "mcp-mock", Capability: "mcp", Config: json.RawMessage(`{}`)})

	exe := runExample(t, s, "mcp_agent",
		[]store.GrantRef{{Capability: "llm", ConfigID: "llm-mock"}, {Capability: "mcp", ConfigID: "mcp-mock"}},
		`{"q":"Add 2 and 3 using the add tool."}`)
	if exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("status=%d err=%s", exe.Status, exe.Error)
	}
	// The mock LLM requests the add tool, the script runs it via MCP (2+3=5), and
	// the LLM's final answer echoes the tool result.
	var out struct {
		Answer string `json:"answer"`
	}
	_ = json.Unmarshal(exe.Output, &out)
	if !strings.Contains(out.Answer, "5") {
		t.Fatalf("expected tool result 5 in answer, got %q (raw %s)", out.Answer, exe.Output)
	}
}
