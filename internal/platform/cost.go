package platform

import (
	"context"
	"encoding/json"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/store"
)

// llmResultUsage is the cost-relevant projection of a memoized llm result.
type llmResultUsage struct {
	Model string `json:"model"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// recordUsage scans a completed execution's event log for llm calls and records a
// usage row (tokens + priced cost) for each. Idempotent across replay via the
// (exec, seq) key. Best-effort: cost tracking never breaks a run.
func (s *Service) recordUsage(ctx context.Context, execID string) {
	exe, err := s.Store.GetExecution(ctx, execID)
	if err != nil {
		return
	}
	owner := ""
	if sc, err := s.Store.GetScript(ctx, exe.ScriptID); err == nil {
		owner = sc.Owner
	}
	events, err := s.Log.Read(ctx, engine.ExecutionID(execID), 0)
	if err != nil {
		return
	}
	for _, ev := range events {
		if ev.Kind != engine.EventRPCResult || ev.RPC == nil || ev.RPC.Capability != "llm" || len(ev.RPC.Result) == 0 {
			continue
		}
		var r llmResultUsage
		if json.Unmarshal(ev.RPC.Result, &r) != nil {
			continue
		}
		in, out, cache := r.Usage.PromptTokens, r.Usage.CompletionTokens, r.Usage.PromptTokensDetails.CachedTokens
		var cost float64
		if s.Pricing != nil {
			cost = s.Pricing.Cost(r.Model, in, out)
		}
		_ = s.Store.PutUsage(ctx, store.UsageRow{
			Exec: execID, Seq: int64(ev.Seq), ScriptID: exe.ScriptID, Workspace: exe.ActorID, Owner: owner,
			Model: r.Model, InputTokens: in, OutputTokens: out, CacheTokens: cache, CostUSD: cost,
		})
	}
}
