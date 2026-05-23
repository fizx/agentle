package platform

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBuildEnvelopeAnonymousActor(t *testing.T) {
	env, ws := buildEnvelope("ex_123", RunRequest{Kind: "dashboard", Data: json.RawMessage(`{"name":"kyle"}`)})
	if ws != "run:ex_123" {
		t.Fatalf("anonymous workspace = %q", ws)
	}
	if env["kind"] != "dashboard" || env["id"] != "ex_123" || env["workspace"] != "run:ex_123" {
		t.Fatalf("envelope = %+v", env)
	}
	data, _ := env["data"].(map[string]any)
	if data["name"] != "kyle" {
		t.Fatalf("data = %+v", env["data"])
	}
}

func TestBuildEnvelopeNamedActorTemplate(t *testing.T) {
	env, ws := buildEnvelope("ex_9", RunRequest{
		Kind:          "webhook",
		ActorTemplate: "webhook-{{event.id}}",
		EventID:       "evt-42",
		Data:          json.RawMessage(`{"x":1}`),
	})
	if ws != "webhook-evt-42" {
		t.Fatalf("named workspace = %q", ws)
	}
	if env["id"] != "evt-42" {
		t.Fatalf("event id = %v", env["id"])
	}
}

func TestInterpolate(t *testing.T) {
	env := map[string]any{
		"id":   "evt-1",
		"data": map[string]any{"user": map[string]any{"id": float64(7)}},
	}
	cases := map[string]string{
		"a-{{event.id}}":           "a-evt-1",
		"u{{event.data.user.id}}":  "u7",
		"{{id}}":                   "evt-1", // leading event. is optional
		"none-{{event.missing}}":   "none-",
		"plain":                    "plain",
		"{{event.data.user.id}}!!": "7!!",
	}
	for tmpl, want := range cases {
		if got := interpolate(tmpl, env); got != want {
			t.Errorf("interpolate(%q) = %q, want %q", tmpl, got, want)
		}
	}
}

func TestActorNamespacingIsolatesAnonymousRuns(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, _ = s.Store.CreateScript(ctx, "s1", "counter", "u1")
	// Counts in per-actor kv and returns the prior value.
	src := `
def main(input):
    c = fetch("c") or 0
    store("c", c + 1)
    return c
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)

	// Two anonymous dashboard runs: each is its own actor => no shared state.
	r1, _ := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	r2, _ := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	if string(r1.Output) != "0" || string(r2.Output) != "0" {
		t.Fatalf("anonymous runs shared kv: r1=%s r2=%s", r1.Output, r2.Output)
	}

	// Two runs bound to the SAME named actor => shared state, counter advances.
	tmpl := "fixed"
	n1, _ := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "webhook", ActorTemplate: tmpl})
	n2, _ := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "webhook", ActorTemplate: tmpl})
	if string(n1.Output) != "0" || string(n2.Output) != "1" {
		t.Fatalf("named actor did not share kv: n1=%s n2=%s", n1.Output, n2.Output)
	}
	if n1.ActorID != "fixed" {
		t.Fatalf("workspace id = %q", n1.ActorID)
	}
}
