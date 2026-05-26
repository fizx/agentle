package store

import (
	"context"
	"database/sql"
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

func TestSecretsScopeMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	// Simulate a pre-round-1 database: secrets keyed on name only, no scope.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE secrets (name TEXT PRIMARY KEY, value TEXT NOT NULL, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO secrets(name,value,created_at) VALUES('LEGACY','v1',1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	// Open via the store: migration must add scope and preserve the row as global.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open/migrate failed: %v", err)
	}
	defer s.Close()

	v, err := s.ResolveSecret(ctx, "LEGACY", "any")
	if err != nil || v != "v1" {
		t.Fatalf("legacy secret lost: v=%q err=%v", v, err)
	}
	// The previously-failing scoped write must now work.
	if err := s.PutSecret(ctx, "NEW", ScriptScope("s1"), "v2"); err != nil {
		t.Fatalf("scoped put after migration: %v", err)
	}
	names, _ := s.ListSecretNames(ctx, ScopeGlobal)
	if len(names) != 1 || names[0] != "LEGACY" {
		t.Fatalf("global names = %v", names)
	}
}

func TestToolConfigDeleteAndSecretExists(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if err := s.PutToolConfig(ctx, ToolConfig{ID: "c1", Capability: "llm", SecretRef: "OPENAI_API_KEY"}); err != nil {
		t.Fatal(err)
	}
	// Secret not set yet.
	if ok, _ := s.SecretExists(ctx, "OPENAI_API_KEY", ScopeGlobal); ok {
		t.Fatal("secret should not exist yet")
	}
	_ = s.PutSecret(ctx, "OPENAI_API_KEY", ScopeGlobal, "sk-x")
	if ok, _ := s.SecretExists(ctx, "OPENAI_API_KEY", ScopeGlobal); !ok {
		t.Fatal("secret should exist after PutSecret")
	}

	// Delete the config.
	if err := s.DeleteToolConfig(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetToolConfig(ctx, "c1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
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
	p1, _ := s.ListScripts(ctx, "", 2, 0)
	p2, _ := s.ListScripts(ctx, "", 2, 2)
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
	list, _ := s.ListExecutions(ctx, "s1", "", 10, 0)
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
	if list[0].ActorID != "exec:ex1" {
		t.Fatalf("actor_id not persisted: %q", list[0].ActorID)
	}
}

func TestChatsCRUD(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	if cs, err := s.ListChats(ctx, "sc1"); err != nil || len(cs) != 0 {
		t.Fatalf("empty list: %v %v", cs, err)
	}
	if err := s.PutChat(ctx, Chat{ID: "ch1", ScriptID: "sc1", ExecID: "ex1", Title: "New chat"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChat(ctx, Chat{ID: "ch2", ScriptID: "sc1", ExecID: "ex2", Title: "Second"}); err != nil {
		t.Fatal(err)
	}
	// A chat on a different script must not leak into sc1's list.
	if err := s.PutChat(ctx, Chat{ID: "ch3", ScriptID: "sc2", ExecID: "ex3"}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.ListChats(ctx, "sc1")
	if err != nil || len(cs) != 2 {
		t.Fatalf("sc1 chats = %d (%v)", len(cs), err)
	}
	if cs[0].ID != "ch1" || cs[1].ID != "ch2" { // ordered by created_at
		t.Fatalf("order = %s,%s", cs[0].ID, cs[1].ID)
	}

	// Rename via upsert.
	got, _ := s.GetChat(ctx, "ch1")
	got.Title = "Renamed"
	if err := s.PutChat(ctx, *got); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetChat(ctx, "ch1"); g.Title != "Renamed" {
		t.Fatalf("rename: %q", g.Title)
	}

	// Archived chats are excluded from the tab list.
	got2, _ := s.GetChat(ctx, "ch2")
	got2.Archived = true
	if err := s.PutChat(ctx, *got2); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.ListChats(ctx, "sc1"); len(cs) != 1 || cs[0].ID != "ch1" {
		t.Fatalf("after archive: %v", cs)
	}

	// Delete removes the row.
	if err := s.DeleteChat(ctx, "ch1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChat(ctx, "ch1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestPluginVersioning(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()

	// First save creates version 1.
	if err := s.PutPlugin(ctx, Plugin{ID: "pl1", Name: "p", Runtime: "python", Source: "v1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPlugin(ctx, "pl1")
	if p.CurrentVersion != 1 || p.Kind != PluginScript {
		t.Fatalf("after first save: ver=%d kind=%q", p.CurrentVersion, p.Kind)
	}

	// A metadata-only change (name/enabled) does NOT bump the version.
	_ = s.PutPlugin(ctx, Plugin{ID: "pl1", Name: "renamed", Runtime: "python", Source: "v1", Enabled: false})
	p, _ = s.GetPlugin(ctx, "pl1")
	if p.CurrentVersion != 1 || p.Name != "renamed" || p.Enabled {
		t.Fatalf("metadata change bumped version or lost metadata: %+v", p)
	}

	// A source change appends version 2.
	_ = s.PutPlugin(ctx, Plugin{ID: "pl1", Name: "renamed", Runtime: "python", Source: "v2", Enabled: true})
	p, _ = s.GetPlugin(ctx, "pl1")
	if p.CurrentVersion != 2 || p.Source != "v2" {
		t.Fatalf("after source change: ver=%d src=%q", p.CurrentVersion, p.Source)
	}

	versions, _ := s.ListPluginVersions(ctx, "pl1")
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Source != "v1" {
		t.Fatalf("unexpected version history: %+v", versions)
	}
	old, err := s.GetPluginVersion(ctx, "pl1", 1)
	if err != nil || old.Source != "v1" {
		t.Fatalf("get version 1: %v %+v", err, old)
	}

	// Delete cascades to versions.
	if err := s.DeletePlugin(ctx, "pl1"); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.ListPluginVersions(ctx, "pl1"); len(vs) != 0 {
		t.Fatalf("versions not deleted: %+v", vs)
	}
}
