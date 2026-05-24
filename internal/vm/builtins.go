package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/kylemaxwell/agentle/internal/engine"
	"go.starlark.net/starlark"
)

// ---------------------------------------------------------------------------
// Stdlib catalog
//
// Every builtin a script can call is registered here, grouped by category. Each
// group lives in its own std_*.go file, so the surface area is easy to inspect
// and to extend: to add a builtin, write its function in the matching std_*.go
// (or a new one) and add a Builtin entry to that file's group slice. Nothing
// else needs to change — predeclared(), Names() (autocomplete) and the
// /api/capabilities endpoint all derive from this catalog.
// ---------------------------------------------------------------------------

// Builtin is one script-callable function in the stdlib catalog.
type Builtin struct {
	Name  string                                                                                              `json:"name"`
	Group string                                                                                              `json:"group"` // category, for docs/inspection
	Doc   string                                                                                              `json:"doc"`   // one-line signature + description
	Fn    func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) `json:"-"`
}

// catalog is the single source of truth for the stdlib, assembled from the
// per-category group slices defined in the std_*.go files.
func catalog() []Builtin {
	var all []Builtin
	all = append(all, groupSystem...)
	all = append(all, groupKV...)
	all = append(all, groupNet...)
	all = append(all, groupMCP...)
	all = append(all, groupActor...)
	all = append(all, groupConcurrency...)
	all = append(all, groupShell...)
	return all
}

// predeclared builds the Starlark global environment from the catalog.
func predeclared() starlark.StringDict {
	d := make(starlark.StringDict, len(catalog()))
	for _, b := range catalog() {
		b := b
		d[b.Name] = starlark.NewBuiltin(b.Name, b.Fn)
	}
	return d
}

// Names returns the builtin names, sorted (used by the dashboard for autocomplete).
func Names() []string {
	c := catalog()
	out := make([]string, 0, len(c))
	for _, b := range c {
		out = append(out, b.Name)
	}
	sort.Strings(out)
	return out
}

// Catalog returns the documented stdlib surface (name/group/doc), sorted by group
// then name. Served by the control plane so the UI can show capability docs.
func Catalog() []Builtin {
	c := catalog()
	sort.SliceStable(c, func(i, j int) bool {
		if c[i].Group != c[j].Group {
			return c[i].Group < c[j].Group
		}
		return c[i].Name < c[j].Name
	})
	return c
}

// ---------------------------------------------------------------------------
// Shared helpers used by every category
// ---------------------------------------------------------------------------

// thread-local keys.
const (
	tlMediator  = "agentle.mediator"
	tlCtx       = "agentle.ctx"
	tlSuspend   = "agentle.suspend"   // holds an *engine.SuspendError when a call parked
	tlDeadlines = "agentle.deadlines" // stack of absolute (unix-nanos) suspension deadlines
)

// deadline stack: deadline(secs, fn) pushes an absolute deadline for the dynamic
// extent of fn; recv() reads the soonest active deadline. Absolute deadlines are
// derived from a memoized now(), so they're stable across suspend/resume.

func deadlinesOf(t *starlark.Thread) []int64 {
	d, _ := t.Local(tlDeadlines).([]int64)
	return d
}

func pushDeadline(t *starlark.Thread, abs int64) {
	t.SetLocal(tlDeadlines, append(append([]int64{}, deadlinesOf(t)...), abs))
}

func popDeadline(t *starlark.Thread) {
	d := deadlinesOf(t)
	if len(d) > 0 {
		t.SetLocal(tlDeadlines, d[:len(d)-1])
	}
}

// effectiveDeadline returns the soonest active deadline (0 = none), so a tighter
// outer deadline still bounds an inner block.
func effectiveDeadline(t *starlark.Thread) int64 {
	var best int64
	for _, d := range deadlinesOf(t) {
		if d > 0 && (best == 0 || d < best) {
			best = d
		}
	}
	return best
}

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
// Starlark value. args must be JSON-marshalable. This is the one bridge from a
// builtin to the durable engine; every capability call goes through it.
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
		// A durable suspension unwinds the whole script. Stash the typed error so
		// the runner can recover it from under Starlark's backtrace wrapping.
		if errors.Is(err, engine.ErrSuspend) {
			t.SetLocal(tlSuspend, err)
		}
		return nil, err
	}
	return unmarshalResult(res)
}

// suspendOf returns the suspension error stashed by callCap, or nil.
func suspendOf(t *starlark.Thread) error {
	if s, ok := t.Local(tlSuspend).(error); ok {
		return s
	}
	return nil
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
