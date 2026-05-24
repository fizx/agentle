package platform

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/sandbox"
	"github.com/kylemaxwell/agentle/internal/store"
)

// TestPluginMCP exercises a sandboxed capability plugin end-to-end: a bash plugin
// providing an MCP tool, granted via an mcp config, called from a script.
func TestPluginMCP(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pool, err := sandbox.NewLocalPool(filepath.Join(t.TempDir(), "sb"), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ls := engine.NewMemLeaser()
	s := New(st, st.EventLog(ls), ls, pool, st.KV(), st.Inbox(), nil, Config{})
	ctx := context.Background()

	// A bash plugin: argv -> $0="plugin" $1=subcmd $2=tool. list prints a tool
	// catalog; call echoes "pong:<tool>".
	pluginSrc := `case "$1" in
  list) echo '[{"name":"ping","description":"ping the plugin","inputSchema":{"type":"object","properties":{}}}]' ;;
  call) echo "pong:$2" ;;
esac`
	if err := st.PutPlugin(ctx, store.Plugin{ID: "pl1", Name: "pinger", Runtime: "bash", Source: pluginSrc, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// An mcp config that points at the plugin instead of an endpoint.
	_ = st.PutToolConfig(ctx, store.ToolConfig{ID: "mcp-plugin", Capability: "mcp", Config: json.RawMessage(`{"plugin_id":"pl1","name":"pinger"}`)})

	_, _ = st.CreateScript(ctx, "s1", "uses-plugin", "u1")
	src := `
def main(input):
    tools = [t["name"] for t in mcp_list_tools()]
    return {"tools": tools, "out": mcp_call("ping")["text"]}
`
	_, _ = st.SaveVersion(ctx, "s1", src, "", []store.GrantRef{{Capability: "mcp", ConfigID: "mcp-plugin"}})

	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("status=%d err=%s", exe.Status, exe.Error)
	}
	var out struct {
		Tools []string `json:"tools"`
		Out   string   `json:"out"`
	}
	_ = json.Unmarshal(exe.Output, &out)
	if len(out.Tools) != 1 || out.Tools[0] != "ping" || out.Out != "pong:ping" {
		t.Fatalf("unexpected output: %s", exe.Output)
	}
}
