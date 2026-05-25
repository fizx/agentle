package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

func TestFeedbackUpsertAndJoin(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, _ = s.CreateScript(ctx, "s1", "x", "u1")
	_, _ = s.SaveVersion(ctx, "s1", "def main(i): return i", "", nil)
	e := Execution{ID: "ex1", ScriptID: "s1", Version: 1, ActorID: "exec:ex1", Status: int(engine.StatusCompleted), Input: json.RawMessage(`{}`)}
	if err := s.CreateExecution(ctx, e); err != nil {
		t.Fatal(err)
	}

	// Unvoted: no feedback joined, GetFeedback is ErrNotFound.
	got, _ := s.GetExecution(ctx, "ex1")
	if got.Feedback != "" {
		t.Fatalf("unvoted feedback = %q", got.Feedback)
	}
	if _, err := s.GetFeedback(ctx, "ex1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	// Vote up with a note.
	if err := s.SetFeedback(ctx, "ex1", FeedbackUp, "nailed it", "u1"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetExecution(ctx, "ex1")
	if got.Feedback != FeedbackUp || got.FeedbackNote != "nailed it" {
		t.Fatalf("after up: feedback=%q note=%q", got.Feedback, got.FeedbackNote)
	}
	// List view carries the label (but not the note).
	list, _ := s.ListExecutions(ctx, "s1", "", 10, 0)
	if len(list) != 1 || list[0].Feedback != FeedbackUp {
		t.Fatalf("list feedback = %+v", list)
	}
	if list[0].FeedbackNote != "" {
		t.Fatalf("list should not carry note, got %q", list[0].FeedbackNote)
	}

	// Re-vote down: latest wins, one row only.
	if err := s.SetFeedback(ctx, "ex1", FeedbackDown, "", "u1"); err != nil {
		t.Fatal(err)
	}
	f, err := s.GetFeedback(ctx, "ex1")
	if err != nil || f.Label != FeedbackDown {
		t.Fatalf("after down: %+v err=%v", f, err)
	}
	if f.CreatedAt == 0 || f.UpdatedAt < f.CreatedAt {
		t.Fatalf("timestamps off: %+v", f)
	}

	// Clear (un-vote).
	if err := s.SetFeedback(ctx, "ex1", "", "", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFeedback(ctx, "ex1"); err != ErrNotFound {
		t.Fatalf("expected cleared, got %v", err)
	}
	got, _ = s.GetExecution(ctx, "ex1")
	if got.Feedback != "" {
		t.Fatalf("after clear, feedback = %q", got.Feedback)
	}
}
