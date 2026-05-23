package vm

import (
	"fmt"

	"go.starlark.net/starlark"
)

// groupActor: message passing between workspaces. recv() is the durable "yield"
// point that pops the next inbox message mid-flow (`yield` is a reserved Starlark
// word). When the inbox is empty, recv durably suspends the execution rather than
// blocking a goroutine: the engine parks it and resumes by replay when a message
// arrives or the optional timeout fires. recv is crash-safe via the call's IdemKey.
var groupActor = []Builtin{
	{Name: "send", Group: "actor", Doc: "send(workspace, data): enqueue a message to a workspace", Fn: bSend},
	{Name: "recv", Group: "actor", Doc: "recv(timeout=...) -> data | None: durably suspend until the next message (or timeout)", Fn: bRecv},
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
	// Turn a relative timeout into an absolute deadline anchored on a *memoized*
	// now(), so the deadline is identical across suspend/resume cycles. Without a
	// timeout, deadline 0 means suspend indefinitely until a message arrives.
	var deadline int64
	if secs > 0 {
		nowVal, err := callCap(t, "time", "now", map[string]any{}, true, false)
		if err != nil {
			return nil, err
		}
		var startNanos int64
		if err := starlark.AsInt(nowVal, &startNanos); err != nil {
			return nil, fmt.Errorf("recv: %w", err)
		}
		deadline = startNanos + int64(secs*float64(1e9))
	}
	// recv is idempotent: the claim is deduped by the call's stable IdemKey, so no
	// write-ahead intent is needed and re-execution on resume is safe.
	return callCap(t, "inbox", "recv", map[string]any{"deadline": deadline}, true, false)
}
