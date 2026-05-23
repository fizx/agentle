package vm

import "go.starlark.net/starlark"

// groupKV: per-workspace durable key/value state. `load` would read better than
// `fetch` but Starlark reserves `load` as a keyword, so the legal triple is
// store/fetch/keys. kv_get/kv_set/kv_list remain as deprecated aliases.
var groupKV = []Builtin{
	{Name: "store", Group: "kv", Doc: "store(key, value): persist a value in this workspace", Fn: bKVSet},
	{Name: "fetch", Group: "kv", Doc: "fetch(key) -> value | None: read a stored value", Fn: bKVGet},
	{Name: "keys", Group: "kv", Doc: "keys(prefix='') -> [str]: list stored keys", Fn: bKVList},
	{Name: "kv_set", Group: "kv", Doc: "deprecated alias of store", Fn: bKVSet},
	{Name: "kv_get", Group: "kv", Doc: "deprecated alias of fetch", Fn: bKVGet},
	{Name: "kv_list", Group: "kv", Doc: "deprecated alias of keys", Fn: bKVList},
}

func bKVGet(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	if err := starlark.UnpackArgs("fetch", args, kwargs, "key", &key); err != nil {
		return nil, err
	}
	return callCap(t, "kv", "get", map[string]any{"key": key}, true, false)
}

func bKVSet(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var key string
	var value starlark.Value
	if err := starlark.UnpackArgs("store", args, kwargs, "key", &key, "value", &value); err != nil {
		return nil, err
	}
	gv, err := starlarkToGo(value)
	if err != nil {
		return nil, err
	}
	return callCap(t, "kv", "set", map[string]any{"key": key, "value": gv}, false, false)
}

func bKVList(t *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var prefix string
	if err := starlark.UnpackArgs("keys", args, kwargs, "prefix?", &prefix); err != nil {
		return nil, err
	}
	return callCap(t, "kv", "list", map[string]any{"prefix": prefix}, true, false)
}
