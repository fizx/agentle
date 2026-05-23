package vm

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// fakeEnv builds an Environment of simple deterministic executors plus counters.
type fakeEnv struct {
	logs    []string
	mu      sync.Mutex
	llmHits int32
	kv      map[string]json.RawMessage
}

func newFakeEnv() (*fakeEnv, engine.Environment) {
	f := &fakeEnv{kv: map[string]json.RawMessage{}}
	env := engine.Environment{
		"log": engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
			var a struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(inv.Args, &a)
			f.mu.Lock()
			f.logs = append(f.logs, a.Message)
			f.mu.Unlock()
			return json.RawMessage(`null`), nil
		}),
		"time": engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
			if inv.Method == "now" {
				return json.RawMessage(`1700000000`), nil
			}
			return json.RawMessage(`null`), nil // sleep
		}),
		"rand": engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
			if inv.Method == "int" {
				return json.RawMessage(`7`), nil
			}
			return json.RawMessage(`0.42`), nil
		}),
		"llm": engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
			atomic.AddInt32(&f.llmHits, 1)
			return json.RawMessage(`{"content":"hi"}`), nil
		}),
		"kv": engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			switch inv.Method {
			case "set":
				var a struct {
					Key   string          `json:"key"`
					Value json.RawMessage `json:"value"`
				}
				_ = json.Unmarshal(inv.Args, &a)
				f.kv[a.Key] = a.Value
				return json.RawMessage(`null`), nil
			case "get":
				var a struct {
					Key string `json:"key"`
				}
				_ = json.Unmarshal(inv.Args, &a)
				if v, ok := f.kv[a.Key]; ok {
					return v, nil
				}
				return json.RawMessage(`null`), nil
			}
			return json.RawMessage(`null`), nil
		}),
	}
	return f, env
}

func runScript(t *testing.T, src string, input string, env engine.Environment) (json.RawMessage, *engine.MemLog, engine.ExecutionID) {
	t.Helper()
	ls := engine.NewMemLeaser()
	log := engine.NewMemLog(ls)
	exec := engine.ExecutionID("e1")
	lease, _ := ls.Acquire(context.Background(), exec)
	m := engine.NewMediator(exec, log, lease, env, nil, nil)
	r := &Runner{}
	out, err := r.Run(context.Background(), m, src, json.RawMessage(input))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out, log, exec
}

func TestBasicScript(t *testing.T) {
	f, env := newFakeEnv()
	src := `
def main(input):
    log("starting", input["name"])
    kv_set("greeting", "hello " + input["name"])
    g = kv_get("greeting")
    n = rand_int(10)
    return {"greeting": g, "n": n, "t": now()}
`
	out, _, _ := runScript(t, src, `{"name":"kyle"}`, env)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["greeting"] != "hello kyle" {
		t.Fatalf("greeting = %v", got["greeting"])
	}
	if got["n"].(float64) != 7 {
		t.Fatalf("n = %v", got["n"])
	}
	if len(f.logs) != 1 || !strings.Contains(f.logs[0], "kyle") {
		t.Fatalf("logs = %v", f.logs)
	}
}

func TestReplayDoesNotReexecuteLLM(t *testing.T) {
	f, env := newFakeEnv()
	src := `
def main(input):
    a = llm([{"role":"user","content":"hi"}])
    return a["content"]
`
	ls := engine.NewMemLeaser()
	log := engine.NewMemLog(ls)
	exec := engine.ExecutionID("e1")
	ctx := context.Background()

	lease, _ := ls.Acquire(ctx, exec)
	m := engine.NewMediator(exec, log, lease, env, nil, nil)
	r := &Runner{}
	out1, err := r.Run(ctx, m, src, json.RawMessage(`null`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out1) != `"hi"` {
		t.Fatalf("out1 = %s", out1)
	}
	if atomic.LoadInt32(&f.llmHits) != 1 {
		t.Fatalf("expected 1 llm hit, got %d", f.llmHits)
	}

	// Replay from the log: the llm call must be memoized, not re-spent.
	events, _ := log.Read(ctx, exec, 0)
	lease2, _ := ls.Acquire(ctx, exec)
	m2 := engine.NewMediator(exec, log, lease2, env, nil, events)
	out2, err := r.Run(ctx, m2, src, json.RawMessage(`null`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != `"hi"` {
		t.Fatalf("out2 = %s", out2)
	}
	if atomic.LoadInt32(&f.llmHits) != 1 {
		t.Fatalf("replay re-spent llm: %d hits", f.llmHits)
	}
}

func TestParallelMap(t *testing.T) {
	f, env := newFakeEnv()
	src := `
def fetch(i):
    return llm([{"role":"user","content":i}])["content"]

def main(input):
    return parallel_map(fetch, input["items"], max_concurrency=3)
`
	out, _, _ := runScript(t, src, `{"items":["a","b","c","d","e"]}`, env)
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 results, got %v", got)
	}
	for i, g := range got {
		if g != "hi" {
			t.Fatalf("result %d = %q", i, g)
		}
	}
	if atomic.LoadInt32(&f.llmHits) != 5 {
		t.Fatalf("expected 5 llm hits, got %d", f.llmHits)
	}
}

func TestMissingMainFails(t *testing.T) {
	_, env := newFakeEnv()
	ls := engine.NewMemLeaser()
	log := engine.NewMemLog(ls)
	lease, _ := ls.Acquire(context.Background(), "e1")
	m := engine.NewMediator("e1", log, lease, env, nil, nil)
	r := &Runner{}
	_, err := r.Run(context.Background(), m, `x = 1`, json.RawMessage(`null`))
	if err == nil {
		t.Fatal("expected error for missing main")
	}
}
