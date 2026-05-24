package vm

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
)

// groupSystem: always-available deterministic stdlib (logging, time, randomness).
// Time and randomness are memoized RPCs so replay is deterministic.
var groupSystem = []Builtin{
	{Name: "log", Group: "system", Doc: "log(*args): write a line to the execution trace", Fn: bLog},
	{Name: "now", Group: "system", Doc: "now() -> int: current unix nanos (memoized)", Fn: bNow},
	{Name: "sleep", Group: "system", Doc: "sleep(seconds): pause (capped, memoized)", Fn: bSleep},
	{Name: "rand", Group: "system", Doc: "rand() -> float: seeded random in [0,1)", Fn: bRand},
	{Name: "rand_int", Group: "system", Doc: "rand_int(n) -> int: seeded random in [0,n)", Fn: bRandInt},
	{Name: "deadline", Group: "system", Doc: "deadline(seconds, fn) -> fn(): run fn with a suspension deadline; recv() inside returns None once it passes", Fn: bDeadline},
}

// bDeadline runs fn with an ambient suspension deadline of `seconds` from now. Any
// recv() during fn (the soonest enclosing deadline wins) suspends only until that
// absolute time, then returns None. The deadline is anchored on a memoized now(),
// so it is identical across suspend/resume.
func bDeadline(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var seconds starlark.Value
	var fn starlark.Callable
	if err := starlark.UnpackArgs("deadline", args, kwargs, "seconds", &seconds, "fn", &fn); err != nil {
		return nil, err
	}
	secs, ok := starlark.AsFloat(seconds)
	if !ok {
		return nil, fmt.Errorf("deadline: seconds must be a number")
	}
	nowVal, err := callCap(t, "time", "now", map[string]any{}, true, false)
	if err != nil {
		return nil, err
	}
	var startNanos int64
	if err := starlark.AsInt(nowVal, &startNanos); err != nil {
		return nil, fmt.Errorf("deadline: %w", err)
	}
	pushDeadline(t, startNanos+int64(secs*float64(1e9)))
	defer popDeadline(t)
	return starlark.Call(t, fn, nil, nil)
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
