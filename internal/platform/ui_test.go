package platform

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
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

func TestUIChatLoop(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	exe := runExample(t, s, "chat_ui", nil, "")
	if exe.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected chat run to suspend, status=%d", exe.Status)
	}
	if ui, _ := s.GetUI(ctx, exe.ID); ui.Kind != "chat" {
		t.Fatalf("expected chat UI, got %+v", ui)
	}

	// Send a message; the bot replies and suspends again.
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
		if m.Role == "assistant" && m.Text == "**HELLO**" {
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
