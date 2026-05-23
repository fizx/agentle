package vm

import (
	"fmt"

	"go.starlark.net/starlark"
)

// groupActor: message passing between workspaces. recv() is the blocking "yield"
// point that pops the next inbox message mid-flow (`yield` is a reserved Starlark
// word). recv is crash-safe via the call's IdemKey.
var groupActor = []Builtin{
	{Name: "send", Group: "actor", Doc: "send(workspace, data): enqueue a message to a workspace", Fn: bSend},
	{Name: "recv", Group: "actor", Doc: "recv(timeout=...) -> data | None: block for the next message", Fn: bRecv},
}

func bSend(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var workspace string
	var data starlark.Value
	if err := starlark.UnpackArgs("send", args, kwargs, "workspace", &workspace, "data", &data); err != nil {
		return nil, err
	}
	gv, err := starlarkToGo(data)
	if err != nil {
		return nil, err
	}
	return callCap(t, "inbox", "send", map[string]any{"workspace": workspace, "data": gv}, false, false)
}

func bRecv(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var timeout starlark.Value
	if err := starlark.UnpackArgs("recv", args, kwargs, "timeout?", &timeout); err != nil {
		return nil, err
	}
	secs := 0.0
	if timeout != nil {
		f, ok := starlark.AsFloat(timeout)
		if !ok {
			return nil, fmt.Errorf("recv: timeout must be a number")
		}
		secs = f
	}
	return callCap(t, "inbox", "recv", map[string]any{"timeout_seconds": secs}, false, false)
}
