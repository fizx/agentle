package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestVersionsAreImmutableAndIncrement(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateScript(ctx, "s1", "hello"); err != nil {
		t.Fatal(err)
	}
	v1, err := s.SaveVersion(ctx, "s1", "def main(i): return 1", "img", nil)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := s.SaveVersion(ctx, "s1", "def main(i): return 2", "img", []GrantRef{{Capability: "http", ConfigID: "c1"}})
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version != 1 || v2.Version != 2 {
		t.Fatalf("versions = %d, %d", v1.Version, v2.Version)
	}
	sc, _ := s.GetScript(ctx, "s1")
	if sc.CurrentVersion != 2 {
		t.Fatalf("current = %d", sc.CurrentVersion)
	}
	got1, _ := s.GetVersion(ctx, "s1", 1)
	if got1.Source != "def main(i): return 1" {
		t.Fatalf("v1 source mutated: %q", got1.Source)
	}
	got2, _ := s.GetVersion(ctx, "s1", 2)
	if len(got2.Grants) != 1 || got2.Grants[0].Capability != "http" {
		t.Fatalf("grants = %+v", got2.Grants)
	}
}

func TestSecretsNeverListed(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.PutSecret(ctx, "OPENAI_KEY", "sk-supersecret"); err != nil {
		t.Fatal(err)
	}
	names, err := s.ListSecretNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "OPENAI_KEY" {
		t.Fatalf("names = %v", names)
	}
	// Value retrievable only via the internal resolver path.
	v, _ := s.GetSecret(ctx, "OPENAI_KEY")
	if v != "sk-supersecret" {
		t.Fatalf("secret value = %q", v)
	}
}

func TestDurableLogReplayAndConflict(t *testing.T) {
	s := openTest(t)
	ls := engine.NewMemLeaser()
	log := s.EventLog(ls)
	ctx := context.Background()
	lease, _ := ls.Acquire(ctx, "e1")

	ev := engine.Event{Kind: engine.EventRPCResult, RPC: &engine.RPCRecord{CallKey: "0", Capability: "llm", Method: "chat"}}
	seq, err := log.Append(ctx, "e1", 0, lease.Token(), ev, true)
	if err != nil || seq != 0 {
		t.Fatalf("append: seq=%d err=%v", seq, err)
	}
	// Duplicate seq => conflict.
	if _, err := log.Append(ctx, "e1", 0, lease.Token(), ev, true); err != engine.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Stale fence => fenced.
	_, _ = ls.Acquire(ctx, "e1") // steal
	if _, err := log.Append(ctx, "e1", 1, lease.Token(), ev, true); err != engine.ErrFenced {
		t.Fatalf("expected ErrFenced, got %v", err)
	}

	events, err := log.Read(ctx, "e1", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("read: n=%d err=%v", len(events), err)
	}
	if events[0].RPC.Capability != "llm" {
		t.Fatalf("roundtrip lost data: %+v", events[0].RPC)
	}
}

func TestExecutionLifecycle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, _ = s.CreateScript(ctx, "s1", "x")
	_, _ = s.SaveVersion(ctx, "s1", "def main(i): return i", "", nil)
	e := Execution{ID: "ex1", ScriptID: "s1", Version: 1, Status: int(engine.StatusRunning), Input: json.RawMessage(`{"a":1}`), Trigger: "manual"}
	if err := s.CreateExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExecutionStatus(ctx, "ex1", int(engine.StatusCompleted), json.RawMessage(`{"ok":true}`), ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetExecution(ctx, "ex1")
	if got.Status != int(engine.StatusCompleted) || string(got.Output) != `{"ok":true}` {
		t.Fatalf("execution = %+v", got)
	}
	list, _ := s.ListExecutions(ctx, "s1", 10)
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
}
