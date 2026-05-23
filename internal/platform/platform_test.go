package platform

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ls := engine.NewMemLeaser()
	return New(st, st.EventLog(ls), ls, nil, st.KV(), nil, Config{})
}

func TestEndToEndRunWithGrant(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	// A mock-backed llm tool config (no base_url => offline mock).
	if err := s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.CreateScript(ctx, "s1", "greeter"); err != nil {
		t.Fatal(err)
	}
	src := `
def main(input):
    log("hello", input["name"])
    kv_set("last", input["name"])
    reply = llm([{"role": "user", "content": "hi " + input["name"]}])
    return {"reply": reply["content"], "last": kv_get("last")}
`
	if _, err := s.Store.SaveVersion(ctx, "s1", src, "", []store.GrantRef{{Capability: "llm", ConfigID: "llm-mock"}}); err != nil {
		t.Fatal(err)
	}

	exe, err := s.RunExecution(ctx, "s1", 0, json.RawMessage(`{"name":"kyle"}`), "manual")
	if err != nil {
		t.Fatal(err)
	}
	if exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("status=%d err=%s", exe.Status, exe.Error)
	}
	var out struct {
		Reply string `json:"reply"`
		Last  string `json:"last"`
	}
	_ = json.Unmarshal(exe.Output, &out)
	if !strings.Contains(out.Reply, "hi kyle") || out.Last != "kyle" {
		t.Fatalf("output = %s", exe.Output)
	}

	// Trace is a projection of the durable log; must contain the llm + kv + log calls.
	tr, err := s.GetTrace(ctx, exe.ID)
	if err != nil {
		t.Fatal(err)
	}
	caps := map[string]int{}
	for _, sp := range tr.Spans {
		if sp.Kind == "result" {
			caps[sp.Capability]++
		}
	}
	if caps["llm"] != 1 || caps["log"] != 1 || caps["kv"] < 2 {
		t.Fatalf("trace caps = %v", caps)
	}
}

func TestUngrantedCapabilityIsDenied(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, _ = s.Store.CreateScript(ctx, "s1", "x")
	// Script calls llm but has NO grant for it.
	src := `
def main(input):
    return llm([{"role":"user","content":"hi"}])
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)
	exe, err := s.RunExecution(ctx, "s1", 0, json.RawMessage(`null`), "manual")
	if err != nil {
		t.Fatal(err)
	}
	if exe.Status != int(engine.StatusFailed) {
		t.Fatalf("expected failure for ungranted llm, got status=%d", exe.Status)
	}
	if !strings.Contains(exe.Error, "not granted") {
		t.Fatalf("error = %q", exe.Error)
	}
}

func TestReplayRecoveryIsDeterministic(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm"})
	_, _ = s.Store.CreateScript(ctx, "s1", "x")
	src := `
def main(input):
    return rand_int(1000000)
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)
	exe, err := s.RunExecution(ctx, "s1", 0, json.RawMessage(`null`), "manual")
	if err != nil {
		t.Fatal(err)
	}
	first := exe.Output

	// Simulate crash recovery: re-run the engine on the same execution id. The
	// memoized rand result must reproduce exactly (no new randomness).
	eng := &engine.Engine{Leaser: s.Leaser, Log: s.Log, Runner: s.Runner, Res: s}
	if _, err := eng.Run(ctx, engine.ExecutionID(exe.ID)); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Store.GetExecution(ctx, exe.ID)
	if string(again.Output) != string(first) {
		t.Fatalf("replay diverged: %s vs %s", first, again.Output)
	}
}
