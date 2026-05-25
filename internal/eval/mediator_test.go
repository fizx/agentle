package eval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// fakeEnv builds an Environment whose executors record calls and echo a tagged
// result, so tests can assert which kind went live.
func fakeEnv(calls *[]string) engine.Environment {
	mk := func(cap string) engine.Executor {
		return engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
			*calls = append(*calls, cap+"."+inv.Method)
			return json.Marshal(map[string]string{"live": cap})
		})
	}
	return engine.Environment{"llm": mk("llm"), "http": mk("http"), "shell": mk("shell")}
}

func httpInv(method, url, body string) engine.Invocation {
	return engine.Invocation{Capability: "http", Method: method, Args: mustArgs(url, body)}
}

func TestMediatorLLMRunsLive(t *testing.T) {
	var calls []string
	m := New(Config{Env: fakeEnv(&calls)})
	out, err := m.Call(context.Background(), engine.Invocation{Capability: "llm", Method: "call", Args: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"live":"llm"}` || len(calls) != 1 || calls[0] != "llm.call" {
		t.Fatalf("llm not live: out=%s calls=%v", out, calls)
	}
	if m.Executed() != 1 {
		t.Fatalf("executed = %d", m.Executed())
	}
}

func TestMediatorHTTPCassetteHit(t *testing.T) {
	var calls []string
	cass := BuildCassette([]engine.Event{
		httpResultEvent("get", "https://api/x", "", `{"status":200,"body":"cached"}`),
	}, DefaultCanon())
	m := New(Config{Env: fakeEnv(&calls), Cassette: cass})

	out, err := m.Call(context.Background(), httpInv("get", "https://api/x", ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"status":200,"body":"cached"}` {
		t.Fatalf("expected cassette replay, got %s", out)
	}
	if len(calls) != 0 {
		t.Fatalf("cassette hit must not go live, calls=%v", calls)
	}
}

func TestMediatorWriteMissFailsClosed(t *testing.T) {
	var calls []string
	// Empty cassette + default (write-all) classifier => every http call gates.
	m := New(Config{Env: fakeEnv(&calls), MissPolicy: MissFail})

	_, err := m.Call(context.Background(), httpInv("post", "https://api/orders", `{}`))
	var se *StopError
	if !errors.As(err, &se) || se.Kind != StopWriteMiss {
		t.Fatalf("expected StopWriteMiss, got %v", err)
	}
	if !errors.Is(err, ErrStopped) {
		t.Fatal("write-miss should satisfy errors.Is(ErrStopped)")
	}
	if len(calls) != 0 {
		t.Fatalf("write-miss must not issue the request, calls=%v", calls)
	}
	if m.Stop() == nil || m.Stop().Kind != StopWriteMiss {
		t.Fatalf("stop not recorded: %v", m.Stop())
	}
}

func TestMediatorReadMissGoesLive(t *testing.T) {
	var calls []string
	// MethodClassifier => GET is a read; read-miss goes live.
	m := New(Config{Env: fakeEnv(&calls), Classify: MethodClassifier{}})
	out, err := m.Call(context.Background(), httpInv("get", "https://api/x", ""))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"live":"http"}` || len(calls) != 1 {
		t.Fatalf("read-miss should go live: out=%s calls=%v", out, calls)
	}
}

func TestMediatorWriteMissGoLivePolicy(t *testing.T) {
	var calls []string
	m := New(Config{Env: fakeEnv(&calls), MissPolicy: MissGoLive})
	out, err := m.Call(context.Background(), httpInv("post", "https://api/orders", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"live":"http"}` || len(calls) != 1 {
		t.Fatalf("go_live should issue: out=%s calls=%v", out, calls)
	}
}

func TestMediatorPinnedClockAndRand(t *testing.T) {
	m := New(Config{Clock: 12345, RandSeed: 7})
	out, _ := m.Call(context.Background(), engine.Invocation{Capability: "time", Method: "now", Args: json.RawMessage(`{}`)})
	if string(out) != "12345" {
		t.Fatalf("pinned now = %s", out)
	}
	// Sleep returns null without blocking.
	out, _ = m.Call(context.Background(), engine.Invocation{Capability: "time", Method: "sleep", Args: json.RawMessage(`{"seconds":100}`)})
	if string(out) != "null" {
		t.Fatalf("sleep = %s", out)
	}
	// Rand is deterministic for a fixed seed: a second mediator with the same seed
	// yields the same stream.
	r1, _ := m.Call(context.Background(), engine.Invocation{Capability: "rand", Method: "int", Args: json.RawMessage(`{"n":1000}`)})
	m2 := New(Config{RandSeed: 7})
	r2, _ := m2.Call(context.Background(), engine.Invocation{Capability: "rand", Method: "int", Args: json.RawMessage(`{"n":1000}`)})
	if string(r1) != string(r2) {
		t.Fatalf("pinned rand not reproducible: %s vs %s", r1, r2)
	}
}

func TestMediatorRecvReplayThenExhaust(t *testing.T) {
	recvs := []json.RawMessage{json.RawMessage(`{"text":"Tokyo"}`), json.RawMessage(`{"text":"$800"}`)}
	m := New(Config{Recvs: recvs})
	recvInv := engine.Invocation{Capability: "inbox", Method: "recv", Args: json.RawMessage(`{"deadline":0}`)}

	for i, want := range []string{`{"text":"Tokyo"}`, `{"text":"$800"}`} {
		out, err := m.Call(context.Background(), recvInv)
		if err != nil || string(out) != want {
			t.Fatalf("recv #%d = %s err=%v", i, out, err)
		}
	}
	// Third recv exhausts the tape.
	_, err := m.Call(context.Background(), recvInv)
	var se *StopError
	if !errors.As(err, &se) || se.Kind != StopRecvExhausted {
		t.Fatalf("expected StopRecvExhausted, got %v", err)
	}
}

func TestMediatorInboxSendIsLive(t *testing.T) {
	var calls []string
	env := fakeEnv(&calls)
	env["inbox"] = engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
		calls = append(calls, "inbox."+inv.Method)
		return json.RawMessage(`null`), nil
	})
	m := New(Config{Env: env})
	if _, err := m.Call(context.Background(), engine.Invocation{Capability: "inbox", Method: "send", Args: json.RawMessage(`{"data":1}`)}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "inbox.send" {
		t.Fatalf("inbox.send should be live: %v", calls)
	}
}

func TestMediatorBudgetSteps(t *testing.T) {
	var calls []string
	m := New(Config{Env: fakeEnv(&calls), Budget: Budget{MaxSteps: 2}})
	llm := engine.Invocation{Capability: "llm", Method: "call", Args: json.RawMessage(`{}`)}
	if _, err := m.Call(context.Background(), llm); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Call(context.Background(), llm); err != nil {
		t.Fatal(err)
	}
	// Third call trips the step cap.
	_, err := m.Call(context.Background(), llm)
	var se *StopError
	if !errors.As(err, &se) || se.Kind != StopBudget {
		t.Fatalf("expected StopBudget, got %v", err)
	}
}

func TestMediatorChildKeysDeterministic(t *testing.T) {
	m := New(Config{})
	if child := m.Child().(*Mediator); child.prefix != "0" {
		t.Fatalf("first child prefix = %q", child.prefix)
	}
	if child := m.Child().(*Mediator); child.prefix != "1" {
		t.Fatalf("second child prefix = %q", child.prefix)
	}
}
