package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/kylemaxwell/agentle/internal/engine"
	"go.starlark.net/starlark"
)

// thread-local keys.
const (
	tlMediator = "agentle.mediator"
	tlCtx      = "agentle.ctx"
)

func mediatorOf(t *starlark.Thread) engine.Mediator {
	return t.Local(tlMediator).(engine.Mediator)
}

func ctxOf(t *starlark.Thread) context.Context {
	if c, ok := t.Local(tlCtx).(context.Context); ok {
		return c
	}
	return context.Background()
}

// callCap performs a memoized capability RPC and converts the result back into a
// Starlark value. args must be JSON-marshalable.
func callCap(t *starlark.Thread, capability, method string, args any, idempotent, mutatesFS bool) (starlark.Value, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	res, err := mediatorOf(t).Call(ctxOf(t), engine.Invocation{
		Capability: capability,
		Method:     method,
		Args:       raw,
		Idempotent: idempotent,
		MutatesFS:  mutatesFS,
	})
	if err != nil {
		return nil, err
	}
	return unmarshalResult(res)
}

// builtins returns the predeclared global environment shared by every script.
func builtins() starlark.StringDict {
	b := starlark.StringDict{
		"log":          starlark.NewBuiltin("log", bLog),
		"now":          starlark.NewBuiltin("now", bNow),
		"sleep":        starlark.NewBuiltin("sleep", bSleep),
		"rand":         starlark.NewBuiltin("rand", bRand),
		"rand_int":     starlark.NewBuiltin("rand_int", bRandInt),
		"http_get":     starlark.NewBuiltin("http_get", bHTTPGet),
		"http_post":    starlark.NewBuiltin("http_post", bHTTPPost),
		"llm":          starlark.NewBuiltin("llm", bLLM),
		// Preferred KV API. `load` would read better than `fetch`, but Starlark
		// reserves `load` as a keyword (the module-load statement), so it can't
		// be an identifier anywhere. `store`/`fetch`/`keys` are the legal pair.
		"store":        starlark.NewBuiltin("store", bKVSet),  // store(key, value)
		"fetch":        starlark.NewBuiltin("fetch", bKVGet),  // fetch(key) -> value
		"keys":         starlark.NewBuiltin("keys", bKVList),  // keys(prefix) -> [key]
		"kv_get":       starlark.NewBuiltin("kv_get", bKVGet), // deprecated alias of fetch
		"kv_set":       starlark.NewBuiltin("kv_set", bKVSet), // deprecated alias of store
		"kv_list":      starlark.NewBuiltin("kv_list", bKVList),
		"shell":        starlark.NewBuiltin("shell", bShell),
		"parallel_map": starlark.NewBuiltin("parallel_map", bParallelMap),
	}
	return b
}

func bLog(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if s, ok := starlark.AsString(a); ok {
			parts = append(parts, s)
		} else {
			parts = append(parts, a.String())
		}
	}
	if _, err := callCap(t, "log", "print", map[string]any{"message": strings.Join(parts, " ")}, true, false); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func bNow(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("now", args, nil); err != nil {
		return nil, err
	}
	return callCap(t, "time", "now", map[string]any{}, true, false)
}

func bSleep(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var seconds starlark.Value
	if err := starlark.UnpackArgs("sleep", args, kwargs, "seconds", &seconds); err != nil {
		return nil, err
	}
	secs, ok := starlark.AsFloat(seconds)
	if !ok {
		return nil, fmt.Errorf("sleep: seconds must be a number")
	}
	return callCap(t, "time", "sleep", map[string]any{"seconds": secs}, true, false)
}

func bRand(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("rand", args, nil); err != nil {
		return nil, err
	}
	return callCap(t, "rand", "float", map[string]any{}, true, false)
}

func bRandInt(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var n int
	if err := starlark.UnpackArgs("rand_int", args, kwargs, "n", &n); err != nil {
		return nil, err
	}
	return callCap(t, "rand", "int", map[string]any{"n": n}, true, false)
}

func bHTTPGet(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var url string
	var headers *starlark.Dict
	if err := starlark.UnpackArgs("http_get", args, kwargs, "url", &url, "headers?", &headers); err != nil {
		return nil, err
	}
	h, err := dictToStringMap(headers)
	if err != nil {
		return nil, err
	}
	return callCap(t, "http", "get", map[string]any{"url": url, "headers": h}, true, false)
}

func bHTTPPost(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var url, body string
	var headers *starlark.Dict
	if err := starlark.UnpackArgs("http_post", args, kwargs, "url", &url, "body?", &body, "headers?", &headers); err != nil {
		return nil, err
	}
	h, err := dictToStringMap(headers)
	if err != nil {
		return nil, err
	}
	return callCap(t, "http", "post", map[string]any{"url": url, "body": body, "headers": h}, false, false)
}

func bLLM(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var messages starlark.Value
	var model string
	var temperature float64
	if err := starlark.UnpackArgs("llm", args, kwargs, "messages", &messages, "model?", &model, "temperature?", &temperature); err != nil {
		return nil, err
	}
	msgs, err := starlarkToGo(messages)
	if err != nil {
		return nil, err
	}
	return callCap(t, "llm", "chat", map[string]any{"messages": msgs, "model": model, "temperature": temperature}, true, false)
}

func bKVGet(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	if err := starlark.UnpackArgs("kv_get", args, kwargs, "key", &key); err != nil {
		return nil, err
	}
	return callCap(t, "kv", "get", map[string]any{"key": key}, true, false)
}

func bKVSet(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	var value starlark.Value
	if err := starlark.UnpackArgs("kv_set", args, kwargs, "key", &key, "value", &value); err != nil {
		return nil, err
	}
	gv, err := starlarkToGo(value)
	if err != nil {
		return nil, err
	}
	return callCap(t, "kv", "set", map[string]any{"key": key, "value": gv}, false, false)
}

func bKVList(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var prefix string
	if err := starlark.UnpackArgs("kv_list", args, kwargs, "prefix?", &prefix); err != nil {
		return nil, err
	}
	return callCap(t, "kv", "list", map[string]any{"prefix": prefix}, true, false)
}

func bShell(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var argv *starlark.List
	var dir string
	var env *starlark.Dict
	if err := starlark.UnpackArgs("shell", args, kwargs, "argv", &argv, "dir?", &dir, "env?", &env); err != nil {
		return nil, err
	}
	gv, err := starlarkToGo(argv)
	if err != nil {
		return nil, err
	}
	e, err := dictToStringMap(env)
	if err != nil {
		return nil, err
	}
	return callCap(t, "shell", "exec", map[string]any{"argv": gv, "dir": dir, "env": e}, false, true)
}

// bParallelMap applies fn to each item concurrently, each in its own deterministic
// call-key subtree, and returns the results in input order.
func bParallelMap(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var fn starlark.Callable
	var items starlark.Iterable
	maxConc := 4
	if err := starlark.UnpackArgs("parallel_map", args, kwargs, "fn", &fn, "items", &items, "max_concurrency?", &maxConc); err != nil {
		return nil, err
	}
	if maxConc < 1 {
		maxConc = 1
	}

	var elems []starlark.Value
	iter := items.Iterate()
	var e starlark.Value
	for iter.Next(&e) {
		elems = append(elems, e)
	}
	iter.Done()

	parent := mediatorOf(t)
	ctx := ctxOf(t)
	fan := parent.Child() // the parallel_map node; children hang off it deterministically
	children := make([]engine.Mediator, len(elems))
	for i := range elems {
		children[i] = fan.Child()
	}

	fn.Freeze()
	results := make([]starlark.Value, len(elems))
	errs := make([]error, len(elems))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup
	for i := range elems {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			item := elems[i]
			item.Freeze()
			ct := &starlark.Thread{Name: fmt.Sprintf("%s#%d", t.Name, i)}
			ct.SetLocal(tlMediator, children[i])
			ct.SetLocal(tlCtx, ctx)
			r, err := starlark.Call(ct, fn, starlark.Tuple{item}, nil)
			results[i], errs[i] = r, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("parallel_map item %d: %w", i, err)
		}
	}
	return starlark.NewList(results), nil
}

func dictToStringMap(d *starlark.Dict) (map[string]string, error) {
	out := map[string]string{}
	if d == nil {
		return out, nil
	}
	for _, item := range d.Items() {
		k, ok := starlark.AsString(item[0])
		if !ok {
			return nil, fmt.Errorf("header/env key must be string")
		}
		v, ok := starlark.AsString(item[1])
		if !ok {
			return nil, fmt.Errorf("header/env value must be string")
		}
		out[k] = v
	}
	return out, nil
}
