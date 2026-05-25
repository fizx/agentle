package vm

import (
	"go.starlark.net/starlark"
)

// groupUI: interactive form/chat. A script declares a UI (ui_chat / ui_form); the
// dashboard renders a panel and exchanges messages with the run over the actor
// inbox (the panel send()s, the script recv()s). ui_say appends to the transcript.
var groupUI = []Builtin{
	{Name: "ui_chat", Group: "ui", Doc: "ui_chat(title='', intro=''): push a chat panel (drive with recv()/ui_say())", Fn: bUIChat},
	{Name: "ui_say", Group: "ui", Doc: "ui_say(text, role='assistant', blocks=None): append a message to the UI transcript (markdown + typed blocks)", Fn: bUISay},
	{Name: "ui_tools", Group: "ui", Doc: "ui_tools(calls, batch): ask the client to run editor tool calls [{id,name,arguments}]; recv() the {tool_results,batch} reply", Fn: bUITools},
	{Name: "ui_form", Group: "ui", Doc: "ui_form(fields) -> values: push a form (modal over any chat), suspend until submitted, then pop it", Fn: bUIForm},
	{Name: "ui_pop", Group: "ui", Doc: "ui_pop(): pop the top UI panel off the stack", Fn: bUIPop},
	{Name: "ui_clear", Group: "ui", Doc: "ui_clear(): remove all UI panels", Fn: bUIClear},
	{Name: "field", Group: "ui", Doc: "field(name, label='', type='text', options=None, required=False, default=None) -> dict: a form field spec", Fn: bField},
}

func bUIChat(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var title, intro string
	if err := starlark.UnpackArgs("ui_chat", args, kwargs, "title?", &title, "intro?", &intro); err != nil {
		return nil, err
	}
	if _, err := callCap(t, "ui", "declare", map[string]any{"kind": "chat", "title": title, "intro": intro}, true, false); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func bUISay(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var text, role string
	var blocks starlark.Value
	if err := starlark.UnpackArgs("ui_say", args, kwargs, "text", &text, "role?", &role, "blocks?", &blocks); err != nil {
		return nil, err
	}
	if role == "" {
		role = "assistant"
	}
	payload := map[string]any{"kind": "say", "role": role, "text": text}
	if blocks != nil && blocks != starlark.None {
		bv, err := starlarkToGo(blocks)
		if err != nil {
			return nil, err
		}
		payload["blocks"] = bv
	}
	if _, err := callCap(t, "ui", "say", payload, true, false); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

// bUITools emits a batch of editor tool calls for the dashboard to execute
// client-side (read_source / apply_edit / run). The script then recv()s the
// {tool_results, batch} reply the client posts back, so the round-trip is durable
// (the result is memoized on the inbox) and replay-safe.
func bUITools(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var calls starlark.Value
	var batch string
	if err := starlark.UnpackArgs("ui_tools", args, kwargs, "calls", &calls, "batch", &batch); err != nil {
		return nil, err
	}
	cv, err := starlarkToGo(calls)
	if err != nil {
		return nil, err
	}
	if _, err := callCap(t, "ui", "tools", map[string]any{"kind": "tools", "batch": batch, "calls": cv}, true, false); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func bUIForm(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var fields starlark.Value
	if err := starlark.UnpackArgs("ui_form", args, kwargs, "fields", &fields); err != nil {
		return nil, err
	}
	fv, err := starlarkToGo(fields)
	if err != nil {
		return nil, err
	}
	if _, err := callCap(t, "ui", "declare", map[string]any{"kind": "form", "fields": fv}, true, false); err != nil {
		return nil, err
	}
	// Suspend until the user submits the form (delivered as an inbox message).
	res, err := callCap(t, "inbox", "recv", map[string]any{"deadline": effectiveDeadline(t)}, true, false)
	if err != nil {
		return nil, err
	}
	// Auto-dismiss the form panel now that it's been submitted.
	if _, err := callCap(t, "ui", "pop", map[string]any{"kind": "pop"}, true, false); err != nil {
		return nil, err
	}
	return res, nil
}

func bUIPop(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("ui_pop", args, kwargs); err != nil {
		return nil, err
	}
	if _, err := callCap(t, "ui", "pop", map[string]any{"kind": "pop"}, true, false); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func bUIClear(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if err := starlark.UnpackArgs("ui_clear", args, kwargs); err != nil {
		return nil, err
	}
	if _, err := callCap(t, "ui", "clear", map[string]any{"kind": "clear"}, true, false); err != nil {
		return nil, err
	}
	return starlark.None, nil
}

func bField(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var name, label, typ string
	var options, def starlark.Value
	var required bool
	if err := starlark.UnpackArgs("field", args, kwargs,
		"name", &name, "label?", &label, "type?", &typ, "options?", &options, "required?", &required, "default?", &def); err != nil {
		return nil, err
	}
	if typ == "" {
		typ = "text"
	}
	if label == "" {
		label = name
	}
	d := starlark.NewDict(6)
	_ = d.SetKey(starlark.String("name"), starlark.String(name))
	_ = d.SetKey(starlark.String("label"), starlark.String(label))
	_ = d.SetKey(starlark.String("type"), starlark.String(typ))
	_ = d.SetKey(starlark.String("required"), starlark.Bool(required))
	if options != nil && options != starlark.None {
		_ = d.SetKey(starlark.String("options"), options)
	}
	if def != nil && def != starlark.None {
		_ = d.SetKey(starlark.String("default"), def)
	}
	return d, nil
}
