package platform

import (
	"encoding/json"
	"strconv"
	"strings"
)

// buildEnvelope constructs the structured event delivered to a script's main()
// and resolves the actor id (the kv namespace) for the run.
//
// Shape: {...metadata, "data": <input>} — metadata is spread at the top level so
// templates can reference it, e.g. {{event.id}} or {{event.data.user}}.
//
// Actor: a trigger's ActorTemplate binds runs to a named actor (shared state);
// without one, the actor is unique per execution (anonymous — no shared kv).
func buildEnvelope(execID string, req RunRequest) (map[string]any, string) {
	kind := req.Kind
	if kind == "" {
		kind = "dashboard"
	}
	eventID := req.EventID
	if eventID == "" {
		eventID = execID
	}
	var data any
	if len(req.Data) > 0 {
		_ = json.Unmarshal(req.Data, &data)
	}
	env := map[string]any{
		"id":         eventID,
		"kind":       kind,
		"trigger_id": req.TriggerID,
		"data":       data,
	}

	actorID := "exec:" + execID // anonymous: unique per run
	if req.ActorTemplate != "" {
		actorID = "actor:" + interpolate(req.ActorTemplate, env)
	}
	env["actor"] = actorID
	return env, actorID
}

func triggerLabel(req RunRequest) string {
	kind := req.Kind
	if kind == "" {
		kind = "dashboard"
	}
	if req.TriggerID != "" {
		return kind + ":" + req.TriggerID
	}
	return kind
}

// interpolate replaces {{path}} tokens with values from the event envelope. A
// leading "event." is optional; paths are dotted (e.g. data.user.id). Missing
// paths resolve to the empty string.
func interpolate(tmpl string, env map[string]any) string {
	var b strings.Builder
	for {
		open := strings.Index(tmpl, "{{")
		if open < 0 {
			b.WriteString(tmpl)
			break
		}
		b.WriteString(tmpl[:open])
		rest := tmpl[open+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			b.WriteString(tmpl[open:]) // unterminated; emit literally
			break
		}
		path := strings.TrimSpace(rest[:end])
		b.WriteString(lookup(env, path))
		tmpl = rest[end+2:]
	}
	return b.String()
}

func lookup(env map[string]any, path string) string {
	path = strings.TrimPrefix(path, "event.")
	var cur any = env
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	return scalarString(cur)
}

func scalarString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		// JSON numbers; format without trailing zeros for integers.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
