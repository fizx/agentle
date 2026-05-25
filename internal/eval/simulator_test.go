package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

func TestParsePersona(t *testing.T) {
	md := `---
on_unknown: improvise_consistent   # or: refuse
style: goal_locked
context: surface
---
You're a budget-conscious traveler, US passport.
You stated: destination Tokyo.`
	p := ParsePersona(md)
	if p.OnUnknown != UnknownImprovise || p.Style != StyleGoalLocked || p.Context != ContextSurface {
		t.Fatalf("knobs = %+v", p)
	}
	if !strings.Contains(p.Prose, "Tokyo") || strings.Contains(p.Prose, "---") {
		t.Fatalf("prose = %q", p.Prose)
	}
}

func TestParsePersonaDefaults(t *testing.T) {
	// No frontmatter => all conservative defaults, whole text is prose.
	p := ParsePersona("just a plain description")
	if p.OnUnknown != UnknownRefuse || p.Style != StyleNaive || p.Context != ContextSurface {
		t.Fatalf("defaults = %+v", p)
	}
	if p.Prose != "just a plain description" {
		t.Fatalf("prose = %q", p.Prose)
	}
	// Unknown values fall back to defaults.
	p2 := ParsePersona("---\nstyle: chaotic\n---\nx")
	if p2.Style != StyleNaive {
		t.Fatalf("bad value should default, got %q", p2.Style)
	}
}

func TestRenderSurfaceIsSurfaceOnly(t *testing.T) {
	sayArgs := func(text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"kind": "say", "role": "assistant", "text": text})
		return b
	}
	steps := []TrajectoryStep{
		{Capability: "ui", Method: "say", Args: sayArgs("Where to?")},
		{Capability: "llm", Method: "call", Result: json.RawMessage(`{"content":"SECRET internal reasoning"}`)},
		{Capability: "http", Method: "get", Args: mustArgs("https://api/secret", "")},
		{Capability: "inbox", Method: "recv", Result: json.RawMessage(`{"text":"Tokyo"}`)},
		{Capability: "ui", Method: "say", Args: sayArgs("Budget?")},
	}
	surface := RenderSurface(steps, ContextSurface)
	if !strings.Contains(surface, "Where to?") || !strings.Contains(surface, "USER: Tokyo") || !strings.Contains(surface, "Budget?") {
		t.Fatalf("surface missing visible content: %s", surface)
	}
	// Internals must NOT leak in surface mode (that would inflate pass rate).
	if strings.Contains(surface, "SECRET internal reasoning") || strings.Contains(surface, "api/secret") {
		t.Fatalf("surface leaked internals: %s", surface)
	}
	// Oracle mode exposes the internal LLM reply (capability-ceiling measurement).
	oracle := RenderSurface(steps, ContextOracle)
	if !strings.Contains(oracle, "SECRET internal reasoning") {
		t.Fatalf("oracle should expose internals: %s", oracle)
	}
}

func TestLLMSimAnswersFromSurface(t *testing.T) {
	var sawSystem, sawUser string
	sim := &LLMSim{
		Persona: ParsePersona("---\non_unknown: refuse\nstyle: goal_locked\n---\nYou want to fly to Tokyo."),
		LLM: fakeLLM(func(sys, usr string) string {
			sawSystem, sawUser = sys, usr
			return "Tokyo, please."
		}),
	}
	out, err := sim.Answer(context.Background(), "ASSISTANT: Where would you like to go?")
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(out, &msg)
	if msg.Text != "Tokyo, please." {
		t.Fatalf("sim answer = %q", msg.Text)
	}
	// The persona knobs must shape the system prompt.
	if !strings.Contains(sawSystem, "do NOT invent") || !strings.Contains(sawSystem, "persistent") {
		t.Fatalf("system prompt missing knob instructions: %s", sawSystem)
	}
	if !strings.Contains(sawSystem, "fly to Tokyo") {
		t.Fatalf("system prompt missing persona prose: %s", sawSystem)
	}
	if !strings.Contains(sawUser, "Where would you like to go?") {
		t.Fatalf("user prompt missing surface: %s", sawUser)
	}
}

func TestMediatorUsesSimulatorForRecv(t *testing.T) {
	// With a sim wired, recv is answered by the sim regardless of any recorded tape.
	sim := &LLMSim{LLM: fakeLLM(func(_, _ string) string { return "simulated answer" })}
	m := New(Config{Sim: sim, Recvs: []json.RawMessage{json.RawMessage(`{"text":"recorded"}`)}})
	out, err := m.Call(context.Background(), engine.Invocation{Capability: "inbox", Method: "recv", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var msg struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(out, &msg)
	if msg.Text != "simulated answer" {
		t.Fatalf("sim not used: %q", msg.Text)
	}
	// The sim has no tape limit: a second recv still answers (no exhaustion stop).
	if _, err := m.Call(context.Background(), engine.Invocation{Capability: "inbox", Method: "recv", Args: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("second sim recv failed: %v", err)
	}
}
