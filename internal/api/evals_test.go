package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/store"
)

func TestAPIPromoteAndEval(t *testing.T) {
	ts := newTestServer(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"city":"Tokyo"}`))
	}))
	defer backend.Close()

	// Configs: mock llm + an http tool allowed to reach the test backend.
	do(t, ts, "PUT", "/api/configs", map[string]any{"id": "llm-mock", "capability": "llm", "config": json.RawMessage(`{}`)})
	do(t, ts, "PUT", "/api/configs", map[string]any{"id": "http-test", "capability": "http",
		"config": json.RawMessage(`{"allow":["127.0.0.1"],"allow_private":true}`)})

	_, data := do(t, ts, "POST", "/api/scripts", map[string]any{"name": "summarizer"})
	var sc store.Script
	mustJSON(t, data, &sc)

	src := "def main(input):\n    r = http_get(\"" + backend.URL + "/data\")\n    reply = llm([{\"role\":\"user\",\"content\":\"summarize: \" + r[\"body\"]}])\n    return {\"summary\": reply[\"content\"], \"raw\": r[\"body\"]}\n"
	resp, body := do(t, ts, "POST", "/api/scripts/"+sc.ID+"/versions", map[string]any{
		"source": src,
		"grants": []map[string]string{{"capability": "http", "config_id": "http-test"}, {"capability": "llm", "config_id": "llm-mock"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("save version: %d %s", resp.StatusCode, body)
	}

	// Golden run.
	_, data = do(t, ts, "POST", "/api/scripts/"+sc.ID+"/run", map[string]any{"input": map[string]any{}})
	var exe store.Execution
	mustJSON(t, data, &exe)
	if exe.Status != 1 {
		t.Fatalf("golden run status=%d err=%s", exe.Status, exe.Error)
	}

	// Label it a success, then promote to a golden.
	do(t, ts, "PUT", "/api/executions/"+exe.ID+"/feedback", map[string]any{"label": "up"})
	resp, data = do(t, ts, "POST", "/api/executions/"+exe.ID+"/promote", nil)
	if resp.StatusCode != 201 {
		t.Fatalf("promote: %d %s", resp.StatusCode, data)
	}
	var g store.Golden
	mustJSON(t, data, &g)
	if g.Label != "success" || g.OriginExec != exe.ID {
		t.Fatalf("golden = %+v", g)
	}

	// It shows up in the script's dataset.
	_, data = do(t, ts, "GET", "/api/scripts/"+sc.ID+"/goldens", nil)
	var goldens []store.Golden
	mustJSON(t, data, &goldens)
	if len(goldens) != 1 {
		t.Fatalf("goldens = %d", len(goldens))
	}

	// Run an eval of the current version against the golden.
	resp, data = do(t, ts, "POST", "/api/goldens/"+g.ID+"/eval?version=1", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("eval: %d %s", resp.StatusCode, data)
	}
	var res struct {
		Completed bool            `json:"completed"`
		Coverage  float64         `json:"coverage"`
		StopKind  string          `json:"stop_kind"`
		Output    json.RawMessage `json:"output"`
	}
	mustJSON(t, data, &res)
	if !res.Completed || res.Coverage != 1.0 {
		t.Fatalf("eval result = %+v", res)
	}
	if !strings.Contains(string(res.Output), "Tokyo") {
		t.Fatalf("eval output = %s", res.Output)
	}

	// Delete the golden.
	resp, _ = do(t, ts, "DELETE", "/api/goldens/"+g.ID, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete golden: %d", resp.StatusCode)
	}
}
