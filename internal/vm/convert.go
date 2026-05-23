package vm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"go.starlark.net/starlark"
)

// starlarkToGo converts a Starlark value into a JSON-encodable Go value. It
// rejects values with no JSON representation (functions, sets, etc.) so that
// capability args are always serializable and hashable for memoization.
func starlarkToGo(v starlark.Value) (any, error) {
	switch x := v.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(x), nil
	case starlark.Int:
		if i, ok := x.Int64(); ok {
			return i, nil
		}
		return float64(x.Float()), nil
	case starlark.Float:
		return float64(x), nil
	case starlark.String:
		return string(x), nil
	case *starlark.List:
		return iterableToGo(x)
	case starlark.Tuple:
		return iterableToGo(x)
	case *starlark.Dict:
		out := make(map[string]any, x.Len())
		for _, item := range x.Items() {
			ks, ok := starlark.AsString(item[0])
			if !ok {
				return nil, fmt.Errorf("dict key must be a string, got %s", item[0].Type())
			}
			gv, err := starlarkToGo(item[1])
			if err != nil {
				return nil, err
			}
			out[ks] = gv
		}
		return out, nil
	default:
		return nil, fmt.Errorf("cannot convert %s to JSON", v.Type())
	}
}

func iterableToGo(it starlark.Iterable) (any, error) {
	out := []any{}
	iter := it.Iterate()
	defer iter.Done()
	var elem starlark.Value
	for iter.Next(&elem) {
		gv, err := starlarkToGo(elem)
		if err != nil {
			return nil, err
		}
		out = append(out, gv)
	}
	return out, nil
}

// goToStarlark converts a value decoded from JSON (via interface{}) into a
// Starlark value.
func goToStarlark(v any) (starlark.Value, error) {
	switch x := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(x), nil
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return starlark.MakeInt64(int64(x)), nil
		}
		return starlark.Float(x), nil
	case int64:
		return starlark.MakeInt64(x), nil
	case string:
		return starlark.String(x), nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return starlark.MakeInt64(i), nil
		}
		f, err := x.Float64()
		if err != nil {
			return nil, err
		}
		return starlark.Float(f), nil
	case []any:
		elems := make([]starlark.Value, len(x))
		for i, e := range x {
			sv, err := goToStarlark(e)
			if err != nil {
				return nil, err
			}
			elems[i] = sv
		}
		return starlark.NewList(elems), nil
	case map[string]any:
		d := starlark.NewDict(len(x))
		for k, e := range x {
			sv, err := goToStarlark(e)
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(k), sv); err != nil {
				return nil, err
			}
		}
		return d, nil
	default:
		return nil, fmt.Errorf("cannot convert %T from JSON to Starlark", v)
	}
}

// marshalArgs encodes a Starlark value as canonical JSON for capability args.
func marshalArgs(v starlark.Value) (json.RawMessage, error) {
	g, err := starlarkToGo(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(g)
}

// unmarshalResult decodes a capability result (json) into a Starlark value.
func unmarshalResult(raw json.RawMessage) (starlark.Value, error) {
	if len(raw) == 0 {
		return starlark.None, nil
	}
	var g any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&g); err != nil {
		return nil, err
	}
	return goToStarlark(g)
}
