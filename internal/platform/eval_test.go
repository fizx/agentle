package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/eval"
	"github.com/kylemaxwell/agentle/internal/store"
)

// seedEvalScript creates a script with an http+llm golden version that does a read
// (http_get) then an llm call, runs it live against srv to record the golden, and
// promotes the run to a golden dataset entry. Returns the script id and golden id.
func seedEvalScript(t *testing.T, s *Service, srv *httptest.Server) (string, string) {
	t.Helper()
	ctx := context.Background()
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm", Config: json.RawMessage(`{}`)})
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "http-test", Capability: "http",
		Config: json.RawMessage(`{"allow":["127.0.0.1"],"allow_private":true}`)})
	_, _ = s.Store.CreateScript(ctx, "s1", "summarizer", "u1")
	src := `
def main(input):
    r = http_get("` + srv.URL + `/data")
    reply = llm([{"role": "user", "content": "summarize: " + r["body"]}])
    return {"summary": reply["content"], "raw": r["body"]}
`
	grants := []store.GrantRef{{Capability: "http", ConfigID: "http-test"}, {Capability: "llm", ConfigID: "llm-mock"}}
	if _, err := s.Store.SaveVersion(ctx, "s1", src, "", grants); err != nil {
		t.Fatal(err)
	}
	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "s1", Kind: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("golden run failed: status=%d err=%s", exe.Status, exe.Error)
	}
	gid := "gold_1"
	if err := s.Store.CreateGolden(ctx, store.Golden{
		ID: gid, ScriptID: "s1", OriginExec: exe.ID, OriginVersion: 1, Label: store.GoldenSuccess,
	}); err != nil {
		t.Fatal(err)
	}
	return "s1", gid
}

func TestRunEvalReplaysCassette(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"city":"Tokyo"}`))
	}))
	_, gid := seedEvalScript(t, s, srv)

	// Close the server: a passing eval now PROVES the HTTP read was replayed from
	// the cassette, not re-fetched live.
	srv.Close()

	res, err := s.RunEval(ctx, EvalRequest{GoldenID: gid, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Completed {
		t.Fatalf("eval did not complete: stop=%s err=%s", res.StopKind, res.Error)
	}
	if res.Coverage != 1.0 {
		t.Fatalf("coverage = %v (executed=%d golden=%d)", res.Coverage, res.Executed, res.GoldenLen)
	}
	if !strings.Contains(string(res.Output), "Tokyo") {
		t.Fatalf("eval output lost replayed body: %s", res.Output)
	}
}

func TestRunEvalWriteMissFailsClosed(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"city":"Tokyo"}`))
	}))
	defer srv.Close()
	_, gid := seedEvalScript(t, s, srv)

	// A new version that invents an external write (POST) absent from the cassette.
	src := `
def main(input):
    http_post("` + srv.URL + `/orders", body="{}")
    return "ordered"
`
	grants := []store.GrantRef{{Capability: "http", ConfigID: "http-test"}, {Capability: "llm", ConfigID: "llm-mock"}}
	if _, err := s.Store.SaveVersion(ctx, "s1", src, "", grants); err != nil {
		t.Fatal(err)
	}

	res, err := s.RunEval(ctx, EvalRequest{GoldenID: gid, Version: 2, MissPolicy: eval.MissFail})
	if err != nil {
		t.Fatal(err)
	}
	if res.Completed {
		t.Fatal("write-miss eval should not complete")
	}
	if res.StopKind != "write_miss" {
		t.Fatalf("stop kind = %q (msg=%s)", res.StopKind, res.StopMsg)
	}
}

// fakeOpenAI serves both an /data endpoint (for http_get) and /chat/completions
// (for the llm cap). It returns a verdict JSON when it sees the judge's system
// prompt, and a normal agent reply otherwise.
func fakeOpenAI(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/data") {
			_, _ = w.Write([]byte(`{"city":"Tokyo"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		content := "The capital is Tokyo."
		switch {
		case strings.Contains(string(body), "objective evaluator"): // judge system prompt marker
			content = `{"pass": true, "reasoning": "transcript mentions Tokyo", "criteria": [{"criterion":"mentions Tokyo","pass":true,"evidence":"Tokyo"}]}`
		case strings.Contains(string(body), "role-playing a USER"): // simulator system prompt marker
			content = "Paris"
		}
		resp := map[string]any{
			"model":   "fake-model",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content}}},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 5},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func seedJudgeScript(t *testing.T, s *Service, srv *httptest.Server, criteria string) string {
	t.Helper()
	ctx := context.Background()
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-real", Capability: "llm",
		Config: json.RawMessage(`{"base_url":"` + srv.URL + `","model":"fake-model"}`)})
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "http-test", Capability: "http",
		Config: json.RawMessage(`{"allow":["127.0.0.1"],"allow_private":true}`)})
	_, _ = s.Store.CreateScript(ctx, "sj", "judged", "u1")
	src := `
def main(input):
    r = http_get("` + srv.URL + `/data")
    reply = llm([{"role": "user", "content": "capital? " + r["body"]}])
    return reply["content"]
`
	grants := []store.GrantRef{{Capability: "http", ConfigID: "http-test"}, {Capability: "llm", ConfigID: "llm-real"}}
	if _, err := s.Store.SaveVersion(ctx, "sj", src, "", grants); err != nil {
		t.Fatal(err)
	}
	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "sj", Kind: "dashboard"})
	if err != nil || exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("judge golden run: status=%d err=%v %s", exe.Status, err, exe.Error)
	}
	gid := "gold_j"
	if err := s.Store.CreateGolden(ctx, store.Golden{
		ID: gid, ScriptID: "sj", OriginExec: exe.ID, OriginVersion: 1, Label: store.GoldenSuccess, Criteria: criteria,
	}); err != nil {
		t.Fatal(err)
	}
	return gid
}

func TestRunEvalWithJudge(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	srv := fakeOpenAI(t)
	defer srv.Close()
	gid := seedJudgeScript(t, s, srv, "The agent must name the capital of Japan (Tokyo).")

	res, err := s.RunEval(ctx, EvalRequest{GoldenID: gid, Version: 1, Judge: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.JudgeError != "" {
		t.Fatalf("judge error: %s", res.JudgeError)
	}
	if res.Verdict == nil || !res.Verdict.Pass {
		t.Fatalf("verdict = %+v", res.Verdict)
	}
	if res.Verdict.Mode != "full" { // completed run => full mode
		t.Fatalf("mode = %q", res.Verdict.Mode)
	}
	if len(res.Verdict.Criteria) != 1 {
		t.Fatalf("criteria = %+v", res.Verdict.Criteria)
	}
}

func TestCalibrateJudge(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	srv := fakeOpenAI(t)
	defer srv.Close()
	gid := seedJudgeScript(t, s, srv, "The agent must name Tokyo.")
	_ = gid

	stats, err := s.CalibrateJudge(ctx, "sj", "")
	if err != nil {
		t.Fatal(err)
	}
	// One success-labeled golden, judge says pass => perfect agreement on n=1.
	if stats.N != 1 || stats.Agreements != 1 || stats.TP != 1 {
		t.Fatalf("calibration = %+v", stats)
	}
}

// seedRecvScript records a golden for a recv-only conversational script: it
// pre-enqueues the user's recorded answer, runs the script in a named workspace so
// recv claims it synchronously, and promotes the run with a persona that wants a
// DIFFERENT destination — so a passing sim eval proves the simulator (not the
// tape) drove the answer.
func seedRecvScript(t *testing.T, s *Service, srv *httptest.Server, persona string) string {
	t.Helper()
	ctx := context.Background()
	// An llm config exists but is NOT granted to the script; the sim finds it as
	// eval infrastructure via infraLLM.
	_ = s.Store.PutToolConfig(ctx, store.ToolConfig{ID: "llm-real", Capability: "llm",
		Config: json.RawMessage(`{"base_url":"` + srv.URL + `","model":"fake-model"}`)})
	_, _ = s.Store.CreateScript(ctx, "sr", "trip", "u1")
	src := `
def main(input):
    ui_say("Where would you like to travel?")
    dest = recv()["text"]
    return {"destination": dest}
`
	if _, err := s.Store.SaveVersion(ctx, "sr", src, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Inbox.Enqueue(ctx, "trip", json.RawMessage(`{"text":"Tokyo"}`)); err != nil {
		t.Fatal(err)
	}
	exe, err := s.RunExecution(ctx, RunRequest{ScriptID: "sr", Kind: "webhook", ActorTemplate: "trip"})
	if err != nil || exe.Status != int(engine.StatusCompleted) {
		t.Fatalf("recv golden run: status=%d err=%v %s", exe.Status, err, exe.Error)
	}
	if !strings.Contains(string(exe.Output), "Tokyo") {
		t.Fatalf("golden did not record the recv: %s", exe.Output)
	}
	gid := "gold_r"
	if err := s.Store.CreateGolden(ctx, store.Golden{
		ID: gid, ScriptID: "sr", OriginExec: exe.ID, OriginVersion: 1, Label: store.GoldenSuccess, Persona: persona,
	}); err != nil {
		t.Fatal(err)
	}
	return gid
}

func TestRunEvalSimulatorAnswersNewQuestions(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	srv := fakeOpenAI(t)
	defer srv.Close()
	persona := "---\non_unknown: refuse\nstyle: naive\n---\nYou are a traveler who wants to visit Paris."
	gid := seedRecvScript(t, s, srv, persona)

	res, err := s.RunEval(ctx, EvalRequest{GoldenID: gid, Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Completed {
		t.Fatalf("sim eval did not complete: stop=%s err=%s", res.StopKind, res.Error)
	}
	// The sim (persona=Paris) drove recv, NOT the recorded tape (Tokyo).
	if !strings.Contains(string(res.Output), "Paris") || strings.Contains(string(res.Output), "Tokyo") {
		t.Fatalf("simulator did not drive recv; output=%s", res.Output)
	}
}

func TestPersonaConsistencyGate(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	srv := fakeOpenAI(t)
	defer srv.Close()
	gid := seedRecvScript(t, s, srv, "---\nstyle: naive\n---\nYou want to visit Paris.")

	// Origin version through the persona completes => reproduces the success outcome.
	cr, err := s.CheckPersonaConsistency(ctx, gid, "")
	if err != nil {
		t.Fatal(err)
	}
	if !cr.Consistent {
		t.Fatalf("expected consistent persona: %s", cr.Detail)
	}
}

func TestRunEvalSamplesAggregates(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"city":"Tokyo"}`))
	}))
	defer srv.Close()
	_, gid := seedEvalScript(t, s, srv)

	suite, err := s.RunEvalSamples(ctx, EvalRequest{GoldenID: gid, Version: 1}, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic golden (cassette replay + mock llm): all 3 samples pass.
	if suite.K != 3 || suite.Passes != 3 || suite.PassRate != 1.0 || suite.Flaky {
		t.Fatalf("suite = %+v", suite)
	}
	if suite.MeanCoverage != 1.0 || len(suite.Samples) != 3 {
		t.Fatalf("coverage/samples = %v / %d", suite.MeanCoverage, len(suite.Samples))
	}
}

func TestToolPolicyGatesEval(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/extra") {
			hits++
		}
		_, _ = w.Write([]byte(`{"city":"Tokyo"}`))
	}))
	defer srv.Close()
	_, gid := seedEvalScript(t, s, srv)

	// A new version reads a NEW url (not in the cassette) via GET.
	src := `
def main(input):
    a = http_get("` + srv.URL + `/data")
    b = http_get("` + srv.URL + `/extra")
    return {"a": a["body"], "b": b["body"]}
`
	grants := []store.GrantRef{{Capability: "http", ConfigID: "http-test"}, {Capability: "llm", ConfigID: "llm-mock"}}
	if _, err := s.Store.SaveVersion(ctx, "s1", src, "", grants); err != nil {
		t.Fatal(err)
	}

	// Without a policy and without AllowReads, the GET read-miss is treated as a
	// write and gates (fail-safe).
	res, _ := s.RunEval(ctx, EvalRequest{GoldenID: gid, Version: 2})
	if res.Completed || res.StopKind != "write_miss" {
		t.Fatalf("default should gate the read-miss: %+v", res)
	}
	if hits != 0 {
		t.Fatalf("gated call must not hit the server, hits=%d", hits)
	}

	// Operator marks GET on this host as a read => the miss now runs live.
	host := strings.TrimPrefix(srv.URL, "http://")
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if err := s.Store.PutToolPolicy(ctx, store.ToolPolicy{Server: host, Tool: "GET", IsWrite: false}); err != nil {
		t.Fatal(err)
	}
	res, _ = s.RunEval(ctx, EvalRequest{GoldenID: gid, Version: 2})
	if !res.Completed {
		t.Fatalf("policy read should let the eval complete: %+v", res)
	}
	if hits != 1 {
		t.Fatalf("policy read should run live once, hits=%d", hits)
	}
}

func TestRunEvalReadMissGoesLiveWhenAllowed(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/extra") {
			hits++
		}
		_, _ = w.Write([]byte(`{"city":"Tokyo"}`))
	}))
	defer srv.Close()
	_, gid := seedEvalScript(t, s, srv)

	// A new version that reads a NEW url not in the cassette. With AllowReads, the
	// read-miss runs live (hits the server) instead of gating.
	src := `
def main(input):
    a = http_get("` + srv.URL + `/data")
    b = http_get("` + srv.URL + `/extra")
    return {"a": a["body"], "b": b["body"]}
`
	grants := []store.GrantRef{{Capability: "http", ConfigID: "http-test"}, {Capability: "llm", ConfigID: "llm-mock"}}
	if _, err := s.Store.SaveVersion(ctx, "s1", src, "", grants); err != nil {
		t.Fatal(err)
	}

	res, err := s.RunEval(ctx, EvalRequest{GoldenID: gid, Version: 2, AllowReads: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Completed {
		t.Fatalf("read-miss-live eval should complete: stop=%s err=%s", res.StopKind, res.Error)
	}
	if hits != 1 {
		t.Fatalf("expected exactly one live read of /extra, got %d", hits)
	}
}
