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
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rdr)
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
		"source": "def main(input):\n    return input[\"trigger\"]\n",
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

func mustJSON(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
}
