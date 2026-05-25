package eval

import (
	"encoding/json"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

func httpResultEvent(method, url, body string, result string) engine.Event {
	args, _ := json.Marshal(httpArgs{URL: url, Body: body})
	return engine.Event{
		Kind: engine.EventRPCResult,
		RPC:  &engine.RPCRecord{Capability: "http", Method: method, Args: args, Result: json.RawMessage(result)},
	}
}

func TestCassetteRecordReplay(t *testing.T) {
	events := []engine.Event{
		httpResultEvent("get", "https://api.example.com/users/1", "", `{"status":200,"body":"alice"}`),
		// A non-http RPC must be ignored.
		{Kind: engine.EventRPCResult, RPC: &engine.RPCRecord{Capability: "llm", Method: "call", Result: json.RawMessage(`{}`)}},
		httpResultEvent("post", "https://api.example.com/orders", `{"item":"x"}`, `{"status":201,"body":"created"}`),
	}
	c := BuildCassette(events, DefaultCanon())
	if c.Len() != 2 {
		t.Fatalf("len = %d, want 2 (llm ignored)", c.Len())
	}

	// Hit on the GET, query-order independent + header-independent.
	e, ok := c.Lookup("get", "https://api.example.com/users/1", "")
	if !ok || string(e.Result) != `{"status":200,"body":"alice"}` {
		t.Fatalf("get hit = %v %s", ok, e.Result)
	}

	// Hit on the POST keyed by body.
	e, ok = c.Lookup("post", "https://api.example.com/orders", `{"item":"x"}`)
	if !ok || string(e.Result) != `{"status":201,"body":"created"}` {
		t.Fatalf("post hit = %v %s", ok, e.Result)
	}

	// A different body is a miss (novel write).
	if _, ok := c.Lookup("post", "https://api.example.com/orders", `{"item":"y"}`); ok {
		t.Fatal("different body should miss")
	}
	// A never-recorded URL is a miss.
	if _, ok := c.Lookup("get", "https://api.example.com/nope", ""); ok {
		t.Fatal("unrecorded url should miss")
	}
}

func TestCassetteCanonicalization(t *testing.T) {
	c := BuildCassette([]engine.Event{
		httpResultEvent("GET", "https://API.Example.com/search?b=2&a=1", "", `{"status":200}`),
	}, CanonOpts{IgnoreQuery: []string{"ts"}, IncludeBody: true, bodyIsSet: true})

	// Case-insensitive method+host, query-order independent.
	if _, ok := c.Lookup("get", "https://api.example.com/search?a=1&b=2", ""); !ok {
		t.Fatal("expected case/order-insensitive hit")
	}
	// Ignored volatile query param must not break the match.
	if _, ok := c.Lookup("get", "https://api.example.com/search?a=1&b=2&ts=999", ""); !ok {
		t.Fatal("ignored query param should still hit")
	}
	// A significant query change is a miss.
	if _, ok := c.Lookup("get", "https://api.example.com/search?a=9&b=2", ""); ok {
		t.Fatal("changed significant query should miss")
	}
}

func TestCassettePollingInOrder(t *testing.T) {
	// Same request recorded twice with different responses (polling).
	c := BuildCassette([]engine.Event{
		httpResultEvent("get", "https://api/x", "", `{"n":1}`),
		httpResultEvent("get", "https://api/x", "", `{"n":2}`),
	}, DefaultCanon())

	e1, _ := c.Lookup("get", "https://api/x", "")
	e2, _ := c.Lookup("get", "https://api/x", "")
	e3, _ := c.Lookup("get", "https://api/x", "") // exhausted: sticks on last
	if string(e1.Result) != `{"n":1}` || string(e2.Result) != `{"n":2}` || string(e3.Result) != `{"n":2}` {
		t.Fatalf("polling order: %s %s %s", e1.Result, e2.Result, e3.Result)
	}
}

func TestCassetteRecordsError(t *testing.T) {
	events := []engine.Event{{
		Kind: engine.EventRPCResult,
		RPC: &engine.RPCRecord{Capability: "http", Method: "get", Args: mustArgs("https://api/down", ""),
			Err: "http: 503"},
	}}
	c := BuildCassette(events, DefaultCanon())
	e, ok := c.Lookup("get", "https://api/down", "")
	if !ok || e.Err != "http: 503" {
		t.Fatalf("recorded error replay = %v %q", ok, e.Err)
	}
}

func mustArgs(url, body string) json.RawMessage {
	b, _ := json.Marshal(httpArgs{URL: url, Body: body})
	return b
}
