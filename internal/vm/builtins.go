package vm

import (
	"context"
	"encoding/json"
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
	Name  string
	Group string // category, for docs/inspection
	Doc   string // one-line signature + description
	Fn    func(*starlark.Thread, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)
}

// catalog is the single source of truth for the stdlib, assembled from the
// per-category group slices defined in the std_*.go files.
func catalog() []Builtin {
	var all []Builtin
	all = append(all, groupSystem...)
	all = append(all, groupKV...)
	all = append(all, groupNet...)
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
		return nil, err
	}
	return unmarshalResult(res)
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
