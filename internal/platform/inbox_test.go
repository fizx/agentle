package platform

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/store"
)

func TestInboxSendRecvSameRun(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, _ = s.Store.CreateScript(ctx, "s1", "echo", "u1")
	src := `
def main(input):
    ws = input["workspace"]
    send(ws, {"i": 1})
    send(ws, {"i": 2})
    return [recv()["i"], recv()["i"]]
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)
	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("status=%d err=%s", exe.Status, exe.Error)
	}
	if string(exe.Output) != "[1,2]" {
		t.Fatalf("output = %s", exe.Output)
	}
}

func TestInboxCrossExecutionNamedWorkspace(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, _ = s.Store.CreateScript(ctx, "s1", "mailbox", "u1")
	src := `
def main(input):
    if input["data"]["act"] == "send":
        send("mbox", {"hi": input["data"]["n"]})
        return "sent"
    return recv()
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)

	// Producer execution sends into the named workspace "mbox".
	prod, err := s.RunExecution(ctx, RunRequest{
		ScriptID: "s1", Kind: "webhook", ActorTemplate: "mbox",
		Data: json.RawMessage(`{"act":"send","n":42}`),
	})
	if err != nil || prod.Status != int(engine.StatusCompleted) {
		t.Fatalf("producer: status=%d err=%s", prod.Status, prod.Error)
	}

	// Consumer execution in the same workspace receives it.
	cons, err := s.RunExecution(ctx, RunRequest{
		ScriptID: "s1", Kind: "webhook", ActorTemplate: "mbox",
		Data: json.RawMessage(`{"act":"recv"}`),
	})
	if err != nil || cons.Status != int(engine.StatusCompleted) {
		t.Fatalf("consumer: status=%d err=%s", cons.Status, cons.Error)
	}
	var msg struct {
		Hi int `json:"hi"`
	}
	_ = json.Unmarshal(cons.Output, &msg)
	if msg.Hi != 42 {
		t.Fatalf("consumer received %s, want hi=42", cons.Output)
	}
}

func TestInboxRecvTimeoutSuspendsThenReturnsNull(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, _ = s.Store.CreateScript(ctx, "s1", "waiter", "u1")
	src := `
def main(input):
    m = deadline(0.2, lambda: recv())
    return {"got": m}
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)
	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	// Empty inbox: recv durably suspends until its deadline (no goroutine blocks).
	if exe.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected suspended, status=%d err=%s", exe.Status, exe.Error)
	}
	// Past the deadline, the dispatcher resumes it and recv times out to null.
	time.Sleep(300 * time.Millisecond)
	s.Pump(ctx, nil)
	exe, _ = s.Store.GetExecution(ctx, exe.ID)
	if exe.Status != int(engine.StatusCompleted) || string(exe.Output) != `{"got":null}` {
		t.Fatalf("status=%d out=%s err=%s", exe.Status, exe.Output, exe.Error)
	}
}

// TestDurableSuspendResumeOnMessage is the headline durable-suspend path: an actor
// recv()s with no message and parks (StatusSuspended) instead of blocking; a later
// message from another execution wakes it via the dispatcher, and it resumes by
// replaying the log and continues from the recv.
func TestDurableSuspendResumeOnMessage(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	_, _ = s.Store.CreateScript(ctx, "s1", "agent", "u1")
	src := `
def main(input):
    if input["data"]["act"] == "send":
        send("agent-1", {"v": input["data"]["v"]})
        return "sent"
    m = recv()              # no timeout: suspend indefinitely until a message
    return {"got": m["v"]}
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)

	// Start the agent in named workspace "agent-1"; empty inbox => durable suspend.
	agent, err := s.RunExecution(ctx, RunRequest{
		ScriptID: "s1", Kind: "webhook", ActorTemplate: "agent-1",
		Data: json.RawMessage(`{"act":"recv"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Status != int(engine.StatusSuspended) {
		t.Fatalf("expected suspended, got status=%d err=%s", agent.Status, agent.Error)
	}
	if _, err := s.Store.GetSuspension(ctx, agent.ID); err != nil {
		t.Fatalf("suspension row not recorded: %v", err)
	}

	// A producer sends a message into the agent's workspace, then the dispatcher
	// resumes the parked agent.
	prod, err := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "webhook", Data: json.RawMessage(`{"act":"send","v":7}`)})
	if err != nil || prod.Status != int(engine.StatusCompleted) {
		t.Fatalf("producer status=%d err=%s", prod.Status, prod.Error)
	}
	s.Pump(ctx, nil)

	agent, _ = s.Store.GetExecution(ctx, agent.ID)
	if agent.Status != int(engine.StatusCompleted) || string(agent.Output) != `{"got":7}` {
		t.Fatalf("resume failed: status=%d out=%s err=%s", agent.Status, agent.Output, agent.Error)
	}
	if _, err := s.Store.GetSuspension(ctx, agent.ID); err != store.ErrNotFound {
		t.Fatalf("suspension row should be cleared after resume, got %v", err)
	}
}

// Direct store-level idempotency check for Claim (recv crash-safety).
func TestInboxClaimIdempotent(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/i.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ib := st.Inbox()
	_ = ib.Enqueue(ctx, "w", json.RawMessage(`{"a":1}`))
	_ = ib.Enqueue(ctx, "w", json.RawMessage(`{"a":2}`))

	// Same idemKey claims the same message twice (replay/retry), not two.
	m1, ok1, _ := ib.Claim(ctx, "w", "k1")
	m2, ok2, _ := ib.Claim(ctx, "w", "k1")
	if !ok1 || !ok2 || string(m1) != string(m2) {
		t.Fatalf("idempotent claim broken: %s vs %s", m1, m2)
	}
	// A different key gets the next message.
	m3, ok3, _ := ib.Claim(ctx, "w", "k2")
	if !ok3 || string(m3) == string(m1) {
		t.Fatalf("second claim should differ: %s", m3)
	}
}
