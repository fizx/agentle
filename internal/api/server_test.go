package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/platform"
	"github.com/kylemaxwell/agentle/internal/store"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ls := engine.NewMemLeaser()
	svc := platform.New(st, st.EventLog(ls), ls, nil, st.KV(), nil, platform.Config{})
	srv := New(svc, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, ts *httptest.Server, method, path string, body any) (*http.Response, []byte) {
	return doAs(t, ts, "", method, path, body)
}

func doAs(t *testing.T, ts *httptest.Server, user, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rdr)
	if user != "" {
		req.Header.Set("X-Agentle-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func TestAPIFullFlow(t *testing.T) {
	ts := newTestServer(t)

	// Create a script.
	_, data := do(t, ts, "POST", "/api/scripts", map[string]any{"name": "greeter"})
	var sc store.Script
	mustJSON(t, data, &sc)
	if sc.ID == "" {
		t.Fatal("no script id")
	}

	// Add a mock llm config.
	resp, _ := do(t, ts, "PUT", "/api/configs", map[string]any{"id": "llm-mock", "capability": "llm", "config": json.RawMessage(`{}`)})
	if resp.StatusCode != 200 {
		t.Fatalf("put config: %d", resp.StatusCode)
	}

	// Save a version granting llm.
	src := `
def main(input):
    log("hi")
    return llm([{"role":"user","content":"yo"}])["content"]
`
	resp, data = do(t, ts, "POST", "/api/scripts/"+sc.ID+"/versions", map[string]any{
		"source": src,
		"grants": []map[string]string{{"capability": "llm", "config_id": "llm-mock"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("save version: %d %s", resp.StatusCode, data)
	}

	// Run it.
	resp, data = do(t, ts, "POST", "/api/scripts/"+sc.ID+"/run", map[string]any{"input": map[string]any{}})
	if resp.StatusCode != 200 {
		t.Fatalf("run: %d %s", resp.StatusCode, data)
	}
	var exe store.Execution
	mustJSON(t, data, &exe)
	if exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("status=%d err=%s", exe.Status, exe.Error)
	}
	if !strings.Contains(string(exe.Output), "yo") {
		t.Fatalf("output=%s", exe.Output)
	}

	// Trace.
	_, data = do(t, ts, "GET", "/api/executions/"+exe.ID+"/trace", nil)
	if !strings.Contains(string(data), `"llm"`) {
		t.Fatalf("trace missing llm span: %s", data)
	}
}

func TestAPISecretsHideValues(t *testing.T) {
	ts := newTestServer(t)
	do(t, ts, "PUT", "/api/secrets", map[string]any{"name": "OPENAI_KEY", "value": "sk-secret-123"})
	_, data := do(t, ts, "GET", "/api/secrets", nil)
	if strings.Contains(string(data), "sk-secret-123") {
		t.Fatalf("secret value leaked in list: %s", data)
	}
	if !strings.Contains(string(data), "OPENAI_KEY") {
		t.Fatalf("secret name missing: %s", data)
	}
}

func TestAPIWebhookTrigger(t *testing.T) {
	ts := newTestServer(t)
	_, data := do(t, ts, "POST", "/api/scripts", map[string]any{
		"name":   "hook",
		"source": "def main(input):\n    return input[\"kind\"]\n",
	})
	var sc store.Script
	mustJSON(t, data, &sc)

	_, data = do(t, ts, "PUT", "/api/triggers", map[string]any{"script_id": sc.ID, "kind": "webhook"})
	var tr store.Trigger
	mustJSON(t, data, &tr)
	if tr.Spec == "" {
		t.Fatal("expected generated webhook token")
	}

	resp, data := do(t, ts, "POST", "/api/hooks/"+tr.Spec, map[string]any{"hello": "world"})
	if resp.StatusCode != 200 {
		t.Fatalf("webhook: %d %s", resp.StatusCode, data)
	}
	var exe store.Execution
	mustJSON(t, data, &exe)
	if string(exe.Output) != `"webhook"` {
		t.Fatalf("webhook output=%s", exe.Output)
	}
}

func TestAPIVersionRestore(t *testing.T) {
	ts := newTestServer(t)
	_, data := do(t, ts, "POST", "/api/scripts", map[string]any{"name": "v", "source": "def main(i): return 1\n"})
	var sc store.Script
	mustJSON(t, data, &sc)
	// v2
	do(t, ts, "POST", "/api/scripts/"+sc.ID+"/versions", map[string]any{"source": "def main(i): return 2\n"})
	// restore v1 -> creates v3 with v1 source
	resp, data := do(t, ts, "POST", "/api/scripts/"+sc.ID+"/versions/1/restore", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("restore: %d %s", resp.StatusCode, data)
	}
	var v store.Version
	mustJSON(t, data, &v)
	if v.Version != 3 || v.Source != "def main(i): return 1\n" {
		t.Fatalf("restored version = %+v", v)
	}
}

func TestAPIDeleteScript(t *testing.T) {
	ts := newTestServer(t)
	_, data := do(t, ts, "POST", "/api/scripts", map[string]any{"name": "doomed"})
	var sc store.Script
	mustJSON(t, data, &sc)
	resp, _ := do(t, ts, "DELETE", "/api/scripts/"+sc.ID, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete status = %d", resp.StatusCode)
	}
	resp, _ = do(t, ts, "GET", "/api/scripts/"+sc.ID, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
}

func TestAPIRBAC(t *testing.T) {
	ts := newTestServer(t)
	// As the default dev-admin, create a real admin and a plain user.
	do(t, ts, "PUT", "/api/users", map[string]any{"id": "admin1", "name": "Admin", "role": "admin"})
	do(t, ts, "PUT", "/api/users", map[string]any{"id": "bob", "name": "Bob", "role": "user"})

	// Bob (user) cannot manage users.
	resp, _ := doAs(t, ts, "bob", "PUT", "/api/users", map[string]any{"name": "X", "role": "user"})
	if resp.StatusCode != 403 {
		t.Fatalf("user creating user: expected 403, got %d", resp.StatusCode)
	}

	// Bob creates a script (owns it) and can version it.
	_, data := doAs(t, ts, "bob", "POST", "/api/scripts", map[string]any{"name": "bobs"})
	var sc store.Script
	mustJSON(t, data, &sc)
	if sc.Owner != "bob" {
		t.Fatalf("owner = %q", sc.Owner)
	}
	resp, _ = doAs(t, ts, "bob", "POST", "/api/scripts/"+sc.ID+"/versions", map[string]any{"source": "def main(i): return 1\n"})
	if resp.StatusCode != 200 {
		t.Fatalf("owner versioning own script: %d", resp.StatusCode)
	}

	// A different non-admin user cannot edit Bob's script.
	do(t, ts, "PUT", "/api/users", map[string]any{"id": "eve", "name": "Eve", "role": "user"})
	resp, _ = doAs(t, ts, "eve", "POST", "/api/scripts/"+sc.ID+"/versions", map[string]any{"source": "x"})
	if resp.StatusCode != 403 {
		t.Fatalf("non-owner editing script: expected 403, got %d", resp.StatusCode)
	}
	// Admin can edit anyone's script.
	resp, _ = doAs(t, ts, "admin1", "POST", "/api/scripts/"+sc.ID+"/versions", map[string]any{"source": "def main(i): return 2\n"})
	if resp.StatusCode != 200 {
		t.Fatalf("admin editing script: %d", resp.StatusCode)
	}
}

func TestAPIScriptScopedSecrets(t *testing.T) {
	ts := newTestServer(t)
	_, data := do(t, ts, "POST", "/api/scripts", map[string]any{"name": "s"})
	var sc store.Script
	mustJSON(t, data, &sc)

	// Global secret (admin) and a script-scoped secret.
	do(t, ts, "PUT", "/api/secrets", map[string]any{"name": "G", "value": "gv"})
	do(t, ts, "PUT", "/api/secrets?script="+sc.ID, map[string]any{"name": "S", "value": "sv"})

	_, data = do(t, ts, "GET", "/api/secrets?script="+sc.ID, nil)
	if !bytes.Contains(data, []byte(`"S"`)) || bytes.Contains(data, []byte(`"G"`)) {
		t.Fatalf("script secret list wrong (should list S not G): %s", data)
	}
	if bytes.Contains(data, []byte("sv")) {
		t.Fatalf("secret value leaked: %s", data)
	}
}

func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}
