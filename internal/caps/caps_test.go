package caps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

func TestHTTPAllowlistAndFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte("pong:" + r.Header.Get("X-Inject")))
	}))
	defer srv.Close()

	// AllowPrivate because httptest binds loopback; inject a header from "secret".
	host := strings.TrimPrefix(srv.URL, "http://")
	host = strings.Split(host, ":")[0]
	exec := HTTP(HTTPConfig{Allow: []string{host}, AllowPrivate: true, Headers: map[string]string{"X-Inject": "secret"}})

	args, _ := json.Marshal(map[string]any{"url": srv.URL})
	res, err := exec.Execute(context.Background(), engine.Invocation{Capability: "http", Method: "get", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	_ = json.Unmarshal(res, &out)
	if out.Status != 201 || out.Body != "pong:secret" {
		t.Fatalf("got status=%d body=%q", out.Status, out.Body)
	}
}

func TestHTTPDeniesUnlistedHost(t *testing.T) {
	exec := HTTP(HTTPConfig{Allow: []string{"api.allowed.com"}, AllowPrivate: true})
	args, _ := json.Marshal(map[string]any{"url": "https://evil.example.com/x"})
	_, err := exec.Execute(context.Background(), engine.Invocation{Capability: "http", Method: "get", Args: args})
	if err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("expected allowlist denial, got %v", err)
	}
}

func TestHTTPBlocksPrivateIP(t *testing.T) {
	// Host is allowlisted but resolves to loopback; SSRF guard must still block.
	exec := HTTP(HTTPConfig{Allow: []string{"localhost"}, AllowPrivate: false})
	args, _ := json.Marshal(map[string]any{"url": "http://localhost:9/x"})
	_, err := exec.Execute(context.Background(), engine.Invocation{Capability: "http", Method: "get", Args: args})
	if err == nil {
		t.Fatal("expected SSRF block for loopback target")
	}
}

func TestLLMMock(t *testing.T) {
	exec := LLM(LLMConfig{}) // no key => mock
	args, _ := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hello there"}},
	})
	res, err := exec.Execute(context.Background(), engine.Invocation{Capability: "llm", Method: "chat", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(res, &out)
	if !strings.Contains(out.Content, "hello there") {
		t.Fatalf("mock content = %q", out.Content)
	}
}

func TestKVRoundTrip(t *testing.T) {
	store := NewMemKV()
	exec := KV(store, "actor1")
	ctx := context.Background()

	setArgs, _ := json.Marshal(map[string]any{"key": "foo", "value": map[string]any{"a": 1}})
	if _, err := exec.Execute(ctx, engine.Invocation{Capability: "kv", Method: "set", Args: setArgs}); err != nil {
		t.Fatal(err)
	}
	getArgs, _ := json.Marshal(map[string]any{"key": "foo"})
	res, err := exec.Execute(ctx, engine.Invocation{Capability: "kv", Method: "get", Args: getArgs})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res), `"a":1`) {
		t.Fatalf("kv get = %s", res)
	}

	// Namespace isolation: a different actor sees nothing.
	other := KV(store, "actor2")
	res2, _ := other.Execute(ctx, engine.Invocation{Capability: "kv", Method: "get", Args: getArgs})
	if string(res2) != `null` {
		t.Fatalf("expected namespace isolation, got %s", res2)
	}
}
