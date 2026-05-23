package caps

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// KVStore is per-actor key/value runtime state. Values are opaque JSON bytes.
// Backends: in-memory (tests/MVP), Redis (prod).
type KVStore interface {
	Get(ctx context.Context, ns, key string) (json.RawMessage, bool, error)
	Set(ctx context.Context, ns, key string, val json.RawMessage) error
	List(ctx context.Context, ns, prefix string) ([]string, error)
}

// KV returns the "kv" capability executor scoped to one actor namespace.
func KV(store KVStore, namespace string) engine.Executor {
	return engine.ExecutorFunc(func(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
		switch inv.Method {
		case "get":
			var a struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			v, ok, err := store.Get(ctx, namespace, a.Key)
			if err != nil {
				return nil, err
			}
			if !ok {
				return json.RawMessage(`null`), nil
			}
			return v, nil
		case "set":
			var a struct {
				Key   string          `json:"key"`
				Value json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			if err := store.Set(ctx, namespace, a.Key, a.Value); err != nil {
				return nil, err
			}
			return json.RawMessage(`null`), nil
		case "list":
			var a struct {
				Prefix string `json:"prefix"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			keys, err := store.List(ctx, namespace, a.Prefix)
			if err != nil {
				return nil, err
			}
			return json.Marshal(keys)
		default:
			return json.RawMessage(`null`), nil
		}
	})
}

// MemKV is an in-memory KVStore.
type MemKV struct {
	mu sync.RWMutex
	m  map[string]map[string]json.RawMessage
}

func NewMemKV() *MemKV { return &MemKV{m: map[string]map[string]json.RawMessage{}} }

func (s *MemKV) Get(_ context.Context, ns, key string) (json.RawMessage, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[ns][key]
	return v, ok, nil
}

func (s *MemKV) Set(_ context.Context, ns, key string, val json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m[ns] == nil {
		s.m[ns] = map[string]json.RawMessage{}
	}
	s.m[ns][key] = val
	return nil
}

func (s *MemKV) List(_ context.Context, ns, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys []string
	for k := range s.m[ns] {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
