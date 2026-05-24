package platform

import (
	"context"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/pricing"
	"github.com/kylemaxwell/agentle/internal/store"
)

func TestCostTrackingAndSpend(t *testing.T) {
	s := newService(t)
	s.Pricing = pricing.New()
	s.Pricing.Set(map[string]pricing.Price{"mock": {PromptPerToken: 0.001, CompletionPerToken: 0.002}})
	ctx := context.Background()

	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm"})
	_, _ = s.Store.CreateScript(ctx, "s1", "greeter", "u1")
	src := `
def main(input):
    return llm([{"role": "user", "content": "hello there friend"}])["content"]
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", []store.GrantRef{{Capability: "llm", ConfigID: "llm-mock"}})

	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	if err != nil || exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("status=%d err=%s", exe.Status, exe.Error)
	}

	// Trace carries per-span + total cost.
	tr, err := s.GetTrace(ctx, exe.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.CostUSD <= 0 {
		t.Fatalf("expected trace cost > 0, got %v", tr.CostUSD)
	}

	// Spend rolls up by every dimension.
	for _, dim := range []string{"model", "script", "workspace", "user"} {
		rows, err := s.Store.Spend(ctx, dim, "", 0)
		if err != nil {
			t.Fatalf("spend by %s: %v", dim, err)
		}
		if len(rows) != 1 || rows[0].Calls != 1 || rows[0].CostUSD <= 0 {
			t.Fatalf("spend by %s = %+v", dim, rows)
		}
	}
	byModel, _ := s.Store.Spend(ctx, "model", "", 0)
	if byModel[0].Key != "mock" {
		t.Fatalf("expected model 'mock', got %q", byModel[0].Key)
	}

	// RBAC owner scoping.
	mine, _ := s.Store.Spend(ctx, "script", "u1", 0)
	if len(mine) != 1 {
		t.Fatalf("owner u1 should see 1 row, got %d", len(mine))
	}
	if other, _ := s.Store.Spend(ctx, "script", "nobody", 0); len(other) != 0 {
		t.Fatalf("owner 'nobody' should see 0 rows, got %d", len(other))
	}
}
