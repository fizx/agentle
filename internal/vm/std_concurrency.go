package vm

import (
	"fmt"
	"sync"

	"github.com/kylemaxwell/agentle/internal/engine"
	"go.starlark.net/starlark"
)

// groupConcurrency: structured concurrency. Each branch runs in its own
// deterministic call-key subtree, so replay is stable regardless of completion
// order.
var groupConcurrency = []Builtin{
	{Name: "parallel_map", Group: "concurrency", Doc: "parallel_map(fn, items, max_concurrency=4) -> [result]", Fn: bParallelMap},
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
			ct.SetLocal(tlDeadlines, deadlinesOf(t)) // inherit any enclosing deadline()
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
