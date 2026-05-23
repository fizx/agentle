package caps

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// Time returns the "time" capability: now() (unix nanos) and sleep(seconds).
// Sleep is capped to keep playtest executions bounded.
func Time(maxSleep time.Duration) engine.Executor {
	return engine.ExecutorFunc(func(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
		switch inv.Method {
		case "now":
			return json.Marshal(time.Now().UnixNano())
		case "sleep":
			var a struct {
				Seconds float64 `json:"seconds"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			d := time.Duration(a.Seconds * float64(time.Second))
			if d > maxSleep {
				d = maxSleep
			}
			if d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return json.RawMessage(`null`), nil
		default:
			return json.RawMessage(`null`), nil
		}
	})
}
