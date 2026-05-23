package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kylemaxwell/agentle/internal/store"
)

func bearer(t *testing.T, ts *httptest.Server, token, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func TestPublicAPIWithToken(t *testing.T) {
	ts := newTestServer(t)

	// Tokens bind to a stored user; create one (seeded instances always have u_admin).
	do(t, ts, "PUT", "/api/users", map[string]any{"id": "u1", "name": "ci-user", "role": "admin"})

	// Create a script (with a runnable version) via the control plane.
	_, data := doAs(t, ts, "u1", "POST", "/api/scripts", map[string]any{
		"name": "echoer", "source": "def main(input):\n    return {\"got\": input[\"data\"]}\n",
	})
	var sc store.Script
	mustJSON(t, data, &sc)

	// Mint an API token for u1.
	resp, data := doAs(t, ts, "u1", "POST", "/api/tokens", map[string]any{"name": "ci"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token status %d: %s", resp.StatusCode, data)
	}
	var tok store.APIToken
	mustJSON(t, data, &tok)
	if tok.Token == "" {
		t.Fatal("token plaintext not returned")
	}

	// No token => 401.
	resp, _ = bearer(t, ts, "", "POST", "/v1/scripts/"+sc.ID+"/runs", map[string]any{"input": 1})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", resp.StatusCode)
	}
	// Bad token => 401.
	resp, _ = bearer(t, ts, "agtl_bogus", "GET", "/v1/me", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with bad token, got %d", resp.StatusCode)
	}

	// Run the script via the public API with the token.
	resp, data = bearer(t, ts, tok.Token, "POST", "/v1/scripts/"+sc.ID+"/runs", map[string]any{"input": map[string]any{"hello": "world"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status %d: %s", resp.StatusCode, data)
	}
	var run runView
	mustJSON(t, data, &run)
	if run.Status != "completed" || string(run.Output) != `{"got":{"hello":"world"}}` {
		t.Fatalf("unexpected run: status=%s out=%s", run.Status, run.Output)
	}

	// Fetch it back via the token.
	resp, data = bearer(t, ts, tok.Token, "GET", "/v1/runs/"+run.ID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get run status %d: %s", resp.StatusCode, data)
	}
	var got runView
	mustJSON(t, data, &got)
	if got.ID != run.ID {
		t.Fatalf("round-trip id mismatch: %s vs %s", got.ID, run.ID)
	}

	// Trace is reachable too.
	resp, _ = bearer(t, ts, tok.Token, "GET", "/v1/runs/"+run.ID+"/trace", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("trace status %d", resp.StatusCode)
	}
}
