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

// TestCodingAgentToolLoop drives the full editor-tool round-trip with the offline
// mock: a user turn → the model emits a tool call → the projection surfaces it as
// pending → the client posts results → the model gives a final answer. This
// verifies the Phase 2 plumbing (pending_tools, transcript suppression of tool
// deliveries, batch clearing) without needing a real tool-capable model.
func TestCodingAgentToolLoop(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

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

	// A user turn carrying the live buffer. Handed tools, the mock responds with a
	// tool call; the harness emits it and parks awaiting the client's results.
	if err := s.PostMessage(ctx, exe.ID, json.RawMessage(`{"text":"add a log line","source":"def main(input):\n    return {}"}`)); err != nil {
		t.Fatal(err)
	}
	ui, _ := s.GetUI(ctx, exe.ID)
	if ui.PendingTools == nil || len(ui.PendingTools.Calls) == 0 {
		t.Fatalf("expected pending editor tool calls, got %+v", ui)
	}
	call := ui.PendingTools.Calls[0].(map[string]any)
	if call["name"] != "read_source" {
		t.Fatalf("expected mock to call read_source first, got %v", call["name"])
	}

	// The client executes the tool and posts the result under the same batch.
	results, _ := json.Marshal(map[string]any{
		"batch": ui.PendingTools.Batch,
		"tool_results": []map[string]any{
			{"id": call["id"], "name": "read_source", "content": "def main(input):\n    return {}"},
		},
	})
	if err := s.PostMessage(ctx, exe.ID, results); err != nil {
		t.Fatal(err)
	}
	ui, _ = s.GetUI(ctx, exe.ID)
	if ui.PendingTools != nil {
		t.Fatalf("expected pending tools cleared once results were posted, got %+v", ui.PendingTools)
	}
	// The tool-results delivery must not appear as a user message.
	for _, m := range ui.Transcript {
		if m.Role == "user" && strings.Contains(m.Text, "tool_results") {
			t.Fatalf("tool-results delivery leaked into the user transcript: %+v", m)
		}
	}
	var replied bool
	for _, m := range ui.Transcript {
		if m.Role == "assistant" && m.Text != "" {
			replied = true
		}
	}
	if !replied {
		t.Fatalf("expected an assistant reply after the tool round-trip: %+v", ui.Transcript)
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
