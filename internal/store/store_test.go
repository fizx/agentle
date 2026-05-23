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
	if _, err := s.CreateScript(ctx, "s1", "hello", "u1"); err != nil {
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
	if err := s.PutSecret(ctx, "OPENAI_KEY", ScopeGlobal, "sk-supersecret"); err != nil {
		t.Fatal(err)
	}
	names, err := s.ListSecretNames(ctx, ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "OPENAI_KEY" {
		t.Fatalf("names = %v", names)
	}
	// Value retrievable only via the internal resolver path.
	v, _ := s.ResolveSecret(ctx, "OPENAI_KEY", "any-script")
	if v != "sk-supersecret" {
		t.Fatalf("secret value = %q", v)
	}
}

func TestSecretScopePrecedence(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if err := s.PutSecret(ctx, "KEY", ScopeGlobal, "global-val"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSecret(ctx, "KEY", ScriptScope("s1"), "script-val"); err != nil {
		t.Fatal(err)
	}
	// Script s1 sees its own scoped value; others fall back to global.
	if v, _ := s.ResolveSecret(ctx, "KEY", "s1"); v != "script-val" {
		t.Fatalf("s1 resolved %q, want script-val", v)
	}
	if v, _ := s.ResolveSecret(ctx, "KEY", "s2"); v != "global-val" {
		t.Fatalf("s2 resolved %q, want global-val", v)
	}
	// Per-script secret list is isolated.
	names, _ := s.ListSecretNames(ctx, ScriptScope("s1"))
	if len(names) != 1 || names[0] != "KEY" {
		t.Fatalf("script names = %v", names)
	}
}

func TestUsersCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "u1", "Alice", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "u2", "Bob", RoleUser); err != nil {
		t.Fatal(err)
	}
	n, _ := s.CountUsers(ctx)
	if n != 2 {
		t.Fatalf("count = %d", n)
	}
	u, _ := s.GetUser(ctx, "u1")
	if u.Role != RoleAdmin {
		t.Fatalf("role = %q", u.Role)
	}
	if err := s.DeleteUser(ctx, "u2"); err != nil {
		t.Fatal(err)
	}
	users, _ := s.ListUsers(ctx)
	if len(users) != 1 {
		t.Fatalf("after delete: %d users", len(users))
	}
}

func TestDeleteScriptCascades(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, _ = s.CreateScript(ctx, "s1", "doomed", "u1")
	_, _ = s.SaveVersion(ctx, "s1", "def main(i): return 1", "", nil)
	_ = s.PutTrigger(ctx, Trigger{ID: "t1", ScriptID: "s1", Kind: "cron", Spec: "* * * * *", Enabled: true})
	_ = s.PutSecret(ctx, "K", ScriptScope("s1"), "v")

	if err := s.DeleteScript(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetScript(ctx, "s1"); err != ErrNotFound {
		t.Fatalf("script still present: %v", err)
	}
	if vs, _ := s.ListVersions(ctx, "s1"); len(vs) != 0 {
		t.Fatalf("versions not cascaded: %d", len(vs))
	}
	if ts, _ := s.ListTriggers(ctx, "", "s1"); len(ts) != 0 {
		t.Fatalf("triggers not cascaded: %d", len(ts))
	}
	if names, _ := s.ListSecretNames(ctx, ScriptScope("s1")); len(names) != 0 {
		t.Fatalf("script secrets not cascaded: %v", names)
	}
}

func TestTriggersAndWebhookLookup(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_ = s.PutTrigger(ctx, Trigger{ID: "w1", ScriptID: "s1", Kind: "webhook", Spec: "tok-123", ActorTemplate: "u-{{event.id}}", Enabled: true})
	_ = s.PutTrigger(ctx, Trigger{ID: "w2", ScriptID: "s1", Kind: "webhook", Spec: "tok-off", Enabled: false})

	got, err := s.FindWebhookTrigger(ctx, "tok-123")
	if err != nil {
		t.Fatal(err)
	}
	if got.ActorTemplate != "u-{{event.id}}" {
		t.Fatalf("actor_template = %q", got.ActorTemplate)
	}
	// Disabled webhook is not resolvable.
	if _, err := s.FindWebhookTrigger(ctx, "tok-off"); err != ErrNotFound {
		t.Fatalf("disabled webhook resolved: %v", err)
	}
	// Filter by script.
	if ts, _ := s.ListTriggers(ctx, "webhook", "s1"); len(ts) != 2 {
		t.Fatalf("list by script = %d", len(ts))
	}
}

func TestScriptPagination(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, _ = s.CreateScript(ctx, "s"+string(rune('a'+i)), "name"+string(rune('a'+i)), "u1")
	}
	p1, _ := s.ListScripts(ctx, 2, 0)
	p2, _ := s.ListScripts(ctx, 2, 2)
	if len(p1) != 2 || len(p2) != 2 {
		t.Fatalf("pagination sizes: %d %d", len(p1), len(p2))
	}
	if p1[0].ID == p2[0].ID {
		t.Fatalf("pages overlap: %s", p1[0].ID)
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
	_, _ = s.CreateScript(ctx, "s1", "x", "u1")
	_, _ = s.SaveVersion(ctx, "s1", "def main(i): return i", "", nil)
	e := Execution{ID: "ex1", ScriptID: "s1", Version: 1, ActorID: "exec:ex1", Status: int(engine.StatusRunning), Input: json.RawMessage(`{"a":1}`), Trigger: "dashboard"}
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
	list, _ := s.ListExecutions(ctx, "s1", 10, 0)
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
	if list[0].ActorID != "exec:ex1" {
		t.Fatalf("actor_id not persisted: %q", list[0].ActorID)
	}
}
