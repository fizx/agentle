// Package vm runs Starlark scripts against the durable-execution engine. The VM
// has zero ambient authority and zero ambient nondeterminism: time, randomness,
// I/O, and the filesystem are reachable only through memoized capability RPCs.
package vm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kylemaxwell/agentle/internal/engine"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Runner is an engine.Runner backed by Starlark. A script defines a top-level
// `main(input)` function; its return value becomes the execution output.
type Runner struct {
	// StepLimit caps Starlark execution steps per Run (0 = engine default).
	StepLimit uint64
}

const defaultStepLimit = 10_000_000

// Run executes source against mediator m. It is deterministic given identical
// memoized results, satisfying the replay invariant.
func (r *Runner) Run(ctx context.Context, m engine.Mediator, source string, input json.RawMessage) (json.RawMessage, error) {
	thread := &starlark.Thread{Name: "main"}
	thread.SetLocal(tlMediator, m)
	thread.SetLocal(tlCtx, ctx)
	limit := r.StepLimit
	if limit == 0 {
		limit = defaultStepLimit
	}
	thread.SetMaxExecutionSteps(limit)

	opts := &syntax.FileOptions{
		Set:             true,
		While:           true,
		TopLevelControl: true,
		GlobalReassign:  true,
		Recursion:       false,
	}

	globals, err := starlark.ExecFileOptions(opts, thread, "main.star", source, predeclared())
	if err != nil {
		return nil, scriptError(err)
	}

	mainVal, ok := globals["main"]
	if !ok {
		return nil, fmt.Errorf("script must define a `main(input)` function")
	}
	mainFn, ok := mainVal.(starlark.Callable)
	if !ok {
		return nil, fmt.Errorf("`main` must be callable, got %s", mainVal.Type())
	}

	inputVal, err := unmarshalResult(input)
	if err != nil {
		return nil, fmt.Errorf("decode input: %w", err)
	}

	ret, err := starlark.Call(thread, mainFn, starlark.Tuple{inputVal}, nil)
	if err != nil {
		return nil, scriptError(err)
	}

	out, err := marshalArgs(ret)
	if err != nil {
		return nil, fmt.Errorf("encode output: %w", err)
	}
	return out, nil
}

// scriptError unwraps Starlark backtraces into a readable message while
// preserving engine sentinel errors (e.g. ErrNonDeterministic) for callers.
func scriptError(err error) error {
	var evalErr *starlark.EvalError
	if ok := asEvalError(err, &evalErr); ok {
		return fmt.Errorf("%s", evalErr.Backtrace())
	}
	return err
}

func asEvalError(err error, target **starlark.EvalError) bool {
	for err != nil {
		if e, ok := err.(*starlark.EvalError); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
