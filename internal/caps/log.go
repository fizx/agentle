package caps

import (
	"context"
	"encoding/json"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// LogSink receives script log lines for an execution (for live tailing). The
// durable record already lives in the event log; this is a convenience fan-out.
type LogSink func(exec engine.ExecutionID, message string)

// Log returns the "log" capability executor. It records the message via sink (if
// any) and returns null. On replay the call is memoized, so logs are not doubled.
func Log(exec engine.ExecutionID, sink LogSink) engine.Executor {
	return engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
		var a struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(inv.Args, &a); err != nil {
			return nil, err
		}
		if sink != nil {
			sink(exec, a.Message)
		}
		return json.RawMessage(`null`), nil
	})
}
