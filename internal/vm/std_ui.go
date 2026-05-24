package vm

import (
	"go.starlark.net/starlark"
)

// groupUI: interactive form/chat. A script declares a UI (ui_chat / ui_form); the
// dashboard renders a panel and exchanges messages with the run over the actor
// inbox (the panel send()s, the script recv()s). ui_say appends to the transcript.
var groupUI = []Builtin{
	{Name: "ui_chat", Group: "ui", Doc: "ui_chat(title='', intro=''): open a chat panel for this run (drive with recv()/ui_say())", Fn: bUIChat},
	{Name: "ui_say", Group: "ui", Doc: "ui_say(text, role='assistant', blocks=None): append a message to the UI transcript (markdown + typed blocks)", Fn: bUISay},
	{Name: "ui_form", Group: "ui", Doc: "ui_form(fields) -> values: show a form and suspend until the user submits it", Fn: bUIForm},
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
	return callCap(t, "inbox", "recv", map[string]any{"deadline": effectiveDeadline(t)}, true, false)
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
