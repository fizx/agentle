package caps

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math/rand"
	"sync"

	"github.com/kylemaxwell/agentle/internal/engine"
)

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
