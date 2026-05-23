// Package caps provides the bound tool instances (Executors) that back script
// capabilities. Secrets are closed over here in Go and never reach the VM.
package caps

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math/rand"
	"sync"
	"time"

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

// Rand returns the "rand" capability, seeded deterministically from the execution
// id so a fresh run is reproducible; results are then memoized like any RPC.
func Rand(exec engine.ExecutionID) engine.Executor {
	h := fnv.New64a()
	h.Write([]byte(exec))
	src := rand.New(rand.NewSource(int64(h.Sum64()))) //nolint:gosec // not security-sensitive
	var mu sync.Mutex
	return engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
		mu.Lock()
		defer mu.Unlock()
		switch inv.Method {
		case "float":
			return json.Marshal(src.Float64())
		case "int":
			var a struct {
				N int64 `json:"n"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			if a.N <= 0 {
				return json.Marshal(0)
			}
			return json.Marshal(src.Int63n(a.N))
		default:
			return json.Marshal(src.Float64())
		}
	})
}
