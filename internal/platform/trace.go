package platform

import (
	"context"
	"encoding/json"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// Span is a user-facing trace span derived from one log event. Because the trace
// is a projection of the event log, secret-refs (which never enter the log) are
// redacted by construction.
type Span struct {
	Seq        engine.Seq `json:"seq"`
	Kind       string     `json:"kind"`       // intent | result | barrier
	Capability string     `json:"capability"` // for rpc spans
	Method     string     `json:"method"`
	CallKey    string     `json:"call_key"`
	WallTime   int64      `json:"wall_time"`
	Args       string     `json:"args,omitempty"`   // preview of the call args
	Result     string     `json:"result,omitempty"` // preview of the memoized result
	Error      string     `json:"error,omitempty"`
	Snapshot   string     `json:"snapshot,omitempty"`

	// LLM cost attributes (set on llm result spans; derived out-of-band).
	Model        string  `json:"model,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

// Trace is the ordered span list for an execution, with a total cost rollup.
type Trace struct {
	Execution string  `json:"execution"`
	Spans     []Span  `json:"spans"`
	CostUSD   float64 `json:"cost_usd"`
}

const previewLimit = 2000

// GetTrace projects the durable event log for exec into a trace.
func (s *Service) GetTrace(ctx context.Context, exec string) (*Trace, error) {
	events, err := s.Log.Read(ctx, engine.ExecutionID(exec), 0)
	if err != nil {
		return nil, err
	}
	t := &Trace{Execution: exec, Spans: make([]Span, 0, len(events))}
	for _, ev := range events {
		sp := Span{Seq: ev.Seq, Kind: ev.Kind.String(), WallTime: ev.WallTime}
		switch {
		case ev.RPC != nil:
			sp.Capability = ev.RPC.Capability
			sp.Method = ev.RPC.Method
			sp.CallKey = ev.RPC.CallKey
			sp.Error = ev.RPC.Err
			if len(ev.RPC.Args) > 0 {
				sp.Args = preview(ev.RPC.Args)
			}
			if len(ev.RPC.Result) > 0 {
				sp.Result = preview(ev.RPC.Result)
				if ev.RPC.Capability == "llm" {
					var r llmResultUsage
					if json.Unmarshal(ev.RPC.Result, &r) == nil {
						sp.Model = r.Model
						sp.InputTokens = r.Usage.PromptTokens
						sp.OutputTokens = r.Usage.CompletionTokens
						if s.Pricing != nil {
							sp.CostUSD = s.Pricing.Cost(r.Model, sp.InputTokens, sp.OutputTokens)
						}
						t.CostUSD += sp.CostUSD
					}
				}
			}
		case ev.Snapshot != nil:
			sp.Snapshot = string(ev.Snapshot.Key)
		}
		t.Spans = append(t.Spans, sp)
	}
	return t, nil
}

func preview(raw json.RawMessage) string {
	if len(raw) > previewLimit {
		return string(raw[:previewLimit]) + "…"
	}
	return string(raw)
}
