package vm

import (
	"go.starlark.net/starlark"
)

// groupActor: message passing between workspaces. recv() is the durable "yield"
// point that pops the next inbox message mid-flow (`yield` is a reserved Starlark
// word). When the inbox is empty, recv durably suspends the execution rather than
// blocking a goroutine: the engine parks it and resumes by replay when a message
// arrives or the optional timeout fires. recv is crash-safe via the call's IdemKey.
var groupActor = []Builtin{
	{Name: "send", Group: "actor", Doc: "send(workspace, data): enqueue a message to a workspace", Fn: bSend},
	{Name: "recv", Group: "actor", Doc: "recv() -> data | None: durably suspend until the next message (bound waits with deadline())", Fn: bRecv},
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
	if err := starlark.UnpackArgs("recv", args, kwargs); err != nil {
		return nil, err
	}
	// Suspend until the next message, or until the soonest enclosing deadline()
	// (0 = none, suspend indefinitely). The deadline is already an absolute,
	// memoized-now()-anchored timestamp, so it's stable across suspend/resume.
	// recv is idempotent: the claim is deduped by the call's stable IdemKey, so no
	// write-ahead intent is needed and re-execution on resume is safe.
	return callCap(t, "inbox", "recv", map[string]any{"deadline": effectiveDeadline(t)}, true, false)
}
