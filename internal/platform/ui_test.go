package platform

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/store"
)

func TestUIFormSubmit(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	exe := runExample(t, s, "form_ui", nil, "")
	if exe.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected form run to suspend awaiting input, got status=%d err=%s", exe.Status, exe.Error)
	}
	ui, err := s.GetUI(ctx, exe.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ui.Kind != "form" || len(ui.Fields) != 3 || !ui.Awaiting {
		t.Fatalf("unexpected form UI: %+v", ui)
	}

	// Submit the form -> the run resumes and returns the submitted values.
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"name":"kyle","color":"blue","subscribe":true}`)); err != nil {
		t.Fatal(err)
	}
	done, _ := s.Store.GetExecution(ctx, exe.ID)
	if done.Status != int(engine.StatusCompleted) {
		t.Fatalf("expected completed after submit, status=%d err=%s", done.Status, done.Error)
	}
	var vals struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	_ = json.Unmarshal(done.Output, &vals)
	if vals.Name != "kyle" || vals.Color != "blue" {
		t.Fatalf("form values not delivered: %s", done.Output)
	}
}

func TestCodingAgentHarness(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	// Backend (A): the coding assistant is itself an agentle script (llm-backed).
	if err := s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	exe := runExample(t, s, "coding_agent", []store.GrantRef{{Capability: "llm", ConfigID: "llm-mock"}}, "")
	if exe.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected coding agent to suspend awaiting input, status=%d err=%s", exe.Status, exe.Error)
	}
	if ui, _ := s.GetUI(ctx, exe.ID); ui.Kind != "chat" {
		t.Fatalf("expected chat panel, got %+v", ui.Panels)
	}

	// The editor sends {text, source}; the harness feeds the source to the LLM.
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"text":"add a log line","source":"def main(input):\n    return {}"}`)); err != nil {
		t.Fatal(err)
	}
	ui, _ := s.GetUI(ctx, exe.ID)
	var replied bool
	for _, m := range ui.Transcript {
		if m.Role == "assistant" && strings.Contains(m.Text, "add a log line") { // mock echoes the user content (which includes the source)
			replied = true
		}
	}
	if !replied {
		t.Fatalf("expected an assistant reply referencing the request: %+v", ui.Transcript)
	}
}

func TestUIStack(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	exe := runExample(t, s, "stacked_ui", nil, "")
	if exe.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected suspend, status=%d err=%s", exe.Status, exe.Error)
	}
	if ui, _ := s.GetUI(ctx, exe.ID); len(ui.Panels) != 1 || ui.Panels[0].Kind != "chat" {
		t.Fatalf("expected [chat] base panel, got %+v", ui.Panels)
	}

	// Open a form over the chat -> stack becomes [chat, form].
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"text":"/profile"}`)); err != nil {
		t.Fatal(err)
	}
	ui, _ := s.GetUI(ctx, exe.ID)
	if len(ui.Panels) != 2 || ui.Panels[0].Kind != "chat" || ui.Panels[1].Kind != "form" {
		t.Fatalf("expected [chat, form] stack, got %+v", ui.Panels)
	}
	if ui.Kind != "form" || len(ui.Fields) != 2 {
		t.Fatalf("expected top=form with 2 fields, got kind=%s fields=%v", ui.Kind, ui.Fields)
	}

	// Submit the form -> it auto-pops, back to [chat], with the say appended.
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"name":"kyle","role":"dev"}`)); err != nil {
		t.Fatal(err)
	}
	ui, _ = s.GetUI(ctx, exe.ID)
	if len(ui.Panels) != 1 || ui.Panels[0].Kind != "chat" {
		t.Fatalf("expected form popped back to [chat], got %+v", ui.Panels)
	}
	var sawSay bool
	for _, m := range ui.Transcript {
		if m.Role == "assistant" && strings.Contains(m.Text, "Nice to meet you, kyle") {
			sawSay = true
		}
	}
	if !sawSay {
		t.Fatalf("expected post-form say in transcript: %+v", ui.Transcript)
	}

	// /quit ends it.
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"text":"/quit"}`)); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.Store.GetExecution(ctx, exe.ID); done.Status != int(engine.StatusCompleted) {
		t.Fatalf("expected completed after /quit, status=%d", done.Status)
	}
}

func TestUIChatLoop(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	// The chat app is LLM-backed; an empty config selects the offline mock.
	if err := s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	exe := runExample(t, s, "chat_ui", []store.GrantRef{{Capability: "llm", ConfigID: "llm-mock"}}, "")
	if exe.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected chat run to suspend, status=%d", exe.Status)
	}
	if ui, _ := s.GetUI(ctx, exe.ID); ui.Kind != "chat" {
		t.Fatalf("expected chat UI, got %+v", ui)
	}

	// Send a message; the assistant replies (mock echoes the user text) and suspends again.
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"text":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if mid, _ := s.Store.GetExecution(ctx, exe.ID); mid.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected still-suspended chat after one turn, status=%d", mid.Status)
	}
	ui, _ := s.GetUI(ctx, exe.ID)
	var sawUser, sawReply bool
	for _, m := range ui.Transcript {
		if m.Role == "user" && m.Text == "hello" {
			sawUser = true
		}
		if m.Role == "assistant" && strings.Contains(m.Text, "hello") {
			sawReply = true
		}
	}
	if !sawUser || !sawReply {
		t.Fatalf("transcript missing turn: %+v", ui.Transcript)
	}

	// /quit ends the session.
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"text":"/quit"}`)); err != nil {
		t.Fatal(err)
	}
	if done, _ := s.Store.GetExecution(ctx, exe.ID); done.Status != int(engine.StatusCompleted) {
		t.Fatalf("expected completed after /quit, status=%d", done.Status)
	}
}
