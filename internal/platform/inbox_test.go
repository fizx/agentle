package platform

import (
	"context"
	"encoding/json"
	"testing"

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
    return [recv(timeout=2)["i"], recv(timeout=2)["i"]]
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
    return recv(timeout=2)
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

func TestInboxRecvTimeoutReturnsNull(t *testing.T) {
	s := newService(t)
	s.Cfg.MaxRecvWait = 0 // executor default applies; timeout arg keeps it short
	ctx := context.Background()
	_, _ = s.Store.CreateScript(ctx, "s1", "waiter", "u1")
	src := `
def main(input):
    m = recv(timeout=0.2)
    return {"got": m}
`
	_, _ = s.Store.SaveVersion(ctx, "s1", src, "", nil)
	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if exe.Status != int(engine.StatusCompleted) || string(exe.Output) != `{"got":null}` {
		t.Fatalf("status=%d out=%s err=%s", exe.Status, exe.Output, exe.Error)
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
