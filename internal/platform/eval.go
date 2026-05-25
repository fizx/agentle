package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/caps"
	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/eval"
	"github.com/kylemaxwell/agentle/internal/store"
)

// EvalRequest runs one sample of a target version against a golden.
type EvalRequest struct {
	GoldenID   string
	Version    uint64               // target version under test; 0 = script's current
	MissPolicy eval.WriteMissPolicy // default MissFail (read-prefix)
	AllowReads bool                 // treat GET/HEAD as reads (run live on miss) instead of fail-safe write
	Budget     eval.Budget          // guardrails; zero fields get defaults
	Judge      bool                 // run the LLM judge over the trajectory
	JudgeModel string               // pinned judge model (empty => the llm config's default)
	Mode       string               // judge mode override; empty => auto (full if completed, else prefix)
	NoSim      bool                 // force the recorded-recv tape even if the golden has a persona
	SimModel   string               // pinned simulator model (empty => the llm config's default)
}

// EvalResult is the outcome of one eval sample: how far the new version got
// through the golden's trajectory and where (if anywhere) it stopped.
type EvalResult struct {
	GoldenID  string          `json:"golden_id"`
	ScriptID  string          `json:"script_id"`
	Version   uint64          `json:"version"`             // target version evaluated
	Label     string          `json:"label"`               // golden's correctness label
	Executed  int             `json:"executed"`            // RPCs the eval completed
	GoldenLen int             `json:"golden_len"`          // RPCs in the golden trajectory (coverage denominator)
	Coverage  float64         `json:"coverage"`            // executed / golden_len, capped at 1.0
	Completed bool            `json:"completed"`           // ran to the end of main() within budget
	StopKind  string          `json:"stop_kind,omitempty"` // "" if completed; else write_miss|recv_exhausted|budget
	StopMsg   string          `json:"stop_msg,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"` // main()'s return value when completed
	Error     string          `json:"error,omitempty"`  // genuine script error inside the segment

	Verdict    *eval.Verdict `json:"verdict,omitempty"`     // LLM-judge ruling, when judging was requested
	JudgeError string        `json:"judge_error,omitempty"` // judging failed (does not fail the eval)
}

const (
	defaultEvalSteps = 500
	defaultEvalWall  = 90 * time.Second
)

// RunEval re-runs a target version against a golden's recorded effects: HTTP is
// replayed from a cassette, the user channel from recorded recvs, clock/random are
// pinned, and the LLM (the thing under test) runs live. It stops at the first
// write-miss (read-prefix), reporting how much of the golden trajectory it covered.
//
// The eval runs entirely outside the durable engine in a throwaway context: a new
// version's local state (kv, sandbox fs) is discarded, so only the gated external
// writes need management.
func (s *Service) RunEval(ctx context.Context, req EvalRequest) (*EvalResult, error) {
	g, err := s.Store.GetGolden(ctx, req.GoldenID)
	if err != nil {
		return nil, err
	}
	origin, err := s.Store.GetExecution(ctx, g.OriginExec)
	if err != nil {
		return nil, fmt.Errorf("golden origin execution: %w", err)
	}
	events, err := s.Log.Read(ctx, engine.ExecutionID(g.OriginExec), 0)
	if err != nil {
		return nil, fmt.Errorf("read golden log: %w", err)
	}

	// The golden's recorded external surface.
	cassette := eval.BuildCassette(events, eval.DefaultCanon())
	recvs := recordedRecvs(events)
	goldenLen := countRPCs(events)

	// Resolve the target version under test.
	version := req.Version
	if version == 0 {
		sc, err := s.Store.GetScript(ctx, g.ScriptID)
		if err != nil {
			return nil, err
		}
		version = sc.CurrentVersion
	}
	v, err := s.Store.GetVersion(ctx, g.ScriptID, version)
	if err != nil {
		return nil, err
	}

	// A throwaway execution context: synthetic id + actor, in-memory kv/inbox so no
	// local state escapes.
	evalExec := engine.ExecutionID("eval_" + uuid.NewString())
	env, err := s.assembleEnv(ctx, evalExec, g.ScriptID, string(evalExec), v.Grants)
	if err != nil {
		return nil, err
	}
	env["kv"] = caps.KV(caps.NewMemKV(), string(evalExec))
	env["inbox"] = throwawayInbox()

	// Shell runs live in a fresh, discarded sandbox.
	if usesShell(v.Grants) && s.Pool != nil {
		sb, err := s.Pool.Acquire(ctx, evalExec, v.Image, nil)
		if err != nil {
			return nil, fmt.Errorf("acquire eval sandbox: %w", err)
		}
		defer s.Pool.Release(ctx, sb, false)
		env["shell"] = engine.ShellExecutor(sb)
	}

	budget := req.Budget
	if budget.MaxSteps == 0 {
		budget.MaxSteps = defaultEvalSteps
	}
	if budget.MaxWall == 0 {
		budget.MaxWall = defaultEvalWall
	}
	// Read/write classification on a cassette miss: the operator's tool_policy
	// table (keyed by host+method) wins; AllowReads is the GET/HEAD fallback for
	// unmatched calls. Default remains write (fail-safe).
	classify := eval.Classifier(eval.HostMethodClassifier{
		Table:          s.toolPolicyTable(ctx),
		MethodFallback: req.AllowReads,
	})

	// The judge and simulator are eval infrastructure, not the script's grants, so
	// they fall back to any configured llm when the script itself grants none.
	infra := s.infraLLM(ctx, g.ScriptID, env)

	// A persona, when present, swaps the recorded-recv tape for a live simulator
	// that answers the new version's actual questions (surface-only).
	var sim eval.UserSim
	simContext := eval.ContextSurface
	if !req.NoSim && g.Persona != "" && infra != nil {
		p := eval.ParsePersona(g.Persona)
		simContext = p.Context
		sim = &eval.LLMSim{LLM: infra, Model: req.SimModel, Persona: p}
	}

	m := eval.New(eval.Config{
		Exec:       evalExec,
		Env:        env,
		Cassette:   cassette,
		Classify:   classify,
		MissPolicy: req.MissPolicy,
		Recvs:      recvs,
		Sim:        sim,
		SimContext: simContext,
		Budget:     budget,
	})

	out, runErr := s.Runner.Run(ctx, m, v.Source, origin.Input)

	res := &EvalResult{
		GoldenID: g.ID, ScriptID: g.ScriptID, Version: version, Label: g.Label,
		Executed: m.Executed(), GoldenLen: goldenLen,
	}
	res.Coverage = coverage(res.Executed, goldenLen)
	// The mediator's Stop() is the source of truth for the boundary: the VM
	// flattens the typed error into a backtrace string, so we don't parse runErr.
	if stop := m.Stop(); stop != nil {
		res.StopKind = stop.Kind.String()
		res.StopMsg = stop.Detail
	} else if runErr != nil {
		res.Error = runErr.Error() // genuine failure inside the evaluable segment
	} else {
		res.Completed = true
		res.Output = out
	}

	if req.Judge {
		v, jerr := judgeEval(ctx, infra, req, g, res, m.Trajectory())
		if jerr != nil {
			res.JudgeError = jerr.Error()
		} else {
			res.Verdict = v
		}
	}
	return res, nil
}

// judgeEval scores an eval's trajectory with the LLM judge. The judging mode is
// auto-derived (full when the run completed, prefix when it stopped at a
// write-miss) unless the request overrides it.
func judgeEval(ctx context.Context, llm engine.Executor, req EvalRequest, g *store.Golden, res *EvalResult, traj []eval.TrajectoryStep) (*eval.Verdict, error) {
	if llm == nil {
		return nil, fmt.Errorf("no llm capability available to judge with")
	}
	mode := req.Mode
	if mode == "" {
		mode = eval.JudgeFull
		if !res.Completed {
			mode = eval.JudgePrefix
		}
	}
	j := &eval.LLMJudge{LLM: llm, Model: req.JudgeModel}
	return j.Judge(ctx, eval.JudgeInput{
		Criteria:    g.Criteria,
		Mode:        mode,
		GoldenLabel: g.Label,
		Trajectory:  traj,
		Output:      res.Output,
		Completed:   res.Completed,
		StopKind:    res.StopKind,
	})
}

// CalibrateJudge measures judge↔human agreement over a script's golden dataset:
// it judges each golden's origin run (full mode, no failure-inversion — we want
// the judge's raw success assessment) and compares the verdict to the human
// label. Only goldens with a rubric are included. This is the gate that must
// clear before verdicts are trusted.
func (s *Service) CalibrateJudge(ctx context.Context, scriptID, model string) (eval.CalibrationStats, error) {
	goldens, err := s.Store.ListGoldens(ctx, scriptID)
	if err != nil {
		return eval.CalibrationStats{}, err
	}
	var pairs []eval.LabelPair
	for _, g := range goldens {
		if g.Criteria == "" {
			continue
		}
		events, err := s.Log.Read(ctx, engine.ExecutionID(g.OriginExec), 0)
		if err != nil {
			continue
		}
		origin, err := s.Store.GetExecution(ctx, g.OriginExec)
		if err != nil {
			continue
		}
		env, _ := s.assembleEnv(ctx, engine.ExecutionID(g.OriginExec), scriptID, g.OriginExec, originGrants(ctx, s, scriptID, g.OriginVersion))
		llm := s.infraLLM(ctx, scriptID, env)
		if llm == nil {
			continue
		}
		j := &eval.LLMJudge{LLM: llm, Model: model}
		v, err := j.Judge(ctx, eval.JudgeInput{
			Criteria:   g.Criteria,
			Mode:       eval.JudgeFull,
			Trajectory: trajectoryFromLog(events),
			Output:     origin.Output,
			Completed:  origin.Status == int(engine.StatusCompleted),
		})
		if err != nil {
			continue
		}
		pairs = append(pairs, eval.LabelPair{Human: g.Label == store.GoldenSuccess, Judge: v.Pass})
	}
	return eval.Calibrate(pairs), nil
}

// ConsistencyResult reports whether a persona reproduces a golden's outcome when
// it drives the golden's own origin version — the gate that makes trusting the
// authored artifact OK.
type ConsistencyResult struct {
	Consistent bool        `json:"consistent"`
	Detail     string      `json:"detail"`
	Result     *EvalResult `json:"result,omitempty"`
}

// CheckPersonaConsistency runs the golden's ORIGIN version through the persona
// simulator and confirms it reproduces the golden's recorded outcome. A human can
// write a plausible-but-wrong persona; authorship does not bypass validation.
//
// With a rubric, "reproduces the outcome" means the (inversion-aware) judge agrees
// with the human label; without one, it falls back to completion status for
// success goldens.
func (s *Service) CheckPersonaConsistency(ctx context.Context, goldenID, model string) (*ConsistencyResult, error) {
	g, err := s.Store.GetGolden(ctx, goldenID)
	if err != nil {
		return nil, err
	}
	if g.Persona == "" {
		return &ConsistencyResult{Consistent: false, Detail: "no persona to validate"}, nil
	}
	res, err := s.RunEval(ctx, EvalRequest{
		GoldenID: goldenID, Version: g.OriginVersion,
		Judge: g.Criteria != "", JudgeModel: model, SimModel: model,
	})
	if err != nil {
		return nil, err
	}
	cr := &ConsistencyResult{Result: res}
	wantSuccess := g.Label == store.GoldenSuccess
	switch {
	case res.Verdict != nil:
		// Verdict is inversion-aware, so pass == "reproduces the labeled outcome".
		cr.Consistent = res.Verdict.Pass == wantSuccess
		cr.Detail = fmt.Sprintf("judge verdict pass=%v vs expected success=%v", res.Verdict.Pass, wantSuccess)
	case g.Criteria == "" && wantSuccess:
		cr.Consistent = res.Completed
		cr.Detail = fmt.Sprintf("no rubric; success golden reproduced=%v (completed=%v)", res.Completed, res.Completed)
	default:
		cr.Detail = "add a rubric (criteria) to validate this golden"
	}
	return cr, nil
}

// DraftPersona autofills a persona.md draft from a golden's recorded transcript.
// It annotates what it inferred (and from which turn) vs what it's guessing, so a
// human knows where to look. It is a DRAFT — never auto-saved over human edits.
func (s *Service) DraftPersona(ctx context.Context, goldenID, model string) (string, error) {
	g, err := s.Store.GetGolden(ctx, goldenID)
	if err != nil {
		return "", err
	}
	events, err := s.Log.Read(ctx, engine.ExecutionID(g.OriginExec), 0)
	if err != nil {
		return "", err
	}
	env, _ := s.assembleEnv(ctx, engine.ExecutionID(g.OriginExec), g.ScriptID, g.OriginExec, originGrants(ctx, s, g.ScriptID, g.OriginVersion))
	llm := s.infraLLM(ctx, g.ScriptID, env)
	if llm == nil {
		return "", fmt.Errorf("no llm capability available to draft a persona")
	}
	surface := eval.RenderSurface(trajectoryFromLog(events), eval.ContextSurface)
	prompt := "From this recorded conversation, draft a persona.md describing the USER. " +
		"Begin with a YAML frontmatter block (--- ... ---) setting on_unknown (refuse|improvise_consistent), " +
		"style (naive|goal_locked), context (surface|oracle). Then prose stating who they are and the facts they provided. " +
		"Clearly mark which facts you INFERRED from a specific turn vs which you are GUESSING. Output only the markdown.\n\nCONVERSATION:\n" + surface
	temp := 0.2
	args, _ := json.Marshal(map[string]any{
		"model": model, "temperature": temp,
		"messages": []map[string]any{{"role": "user", "content": prompt}},
	})
	out, err := llm.Execute(ctx, engine.Invocation{Capability: "llm", Method: "call", Args: args})
	if err != nil {
		return "", err
	}
	var res struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return "", err
	}
	return res.Content, nil
}

// toolPolicyTable snapshots the operator's read/write classifications into the
// (host+method)->isWrite map the eval classifier consults. Best-effort: a read
// error yields an empty table, so everything defaults to write (fail-safe).
func (s *Service) toolPolicyTable(ctx context.Context) map[string]bool {
	rows, err := s.Store.ListToolPolicies(ctx)
	if err != nil {
		return nil
	}
	t := make(map[string]bool, len(rows))
	for _, p := range rows {
		t[eval.PolicyKey(p.Server, p.Tool)] = p.IsWrite
	}
	return t
}

// infraLLM returns an llm executor for eval infrastructure (judge + simulator):
// the script's own llm grant if it has one, else the first configured llm tool
// config (with its secret resolved). Judge/sim are eval infra, not script grants,
// so a recv-only script with no llm grant can still be simulated and judged.
func (s *Service) infraLLM(ctx context.Context, scriptID string, env engine.Environment) engine.Executor {
	if env != nil && env["llm"] != nil {
		return env["llm"]
	}
	configs, err := s.Store.ListToolConfigs(ctx)
	if err != nil {
		return nil
	}
	for i := range configs {
		c := configs[i]
		if c.Capability != "llm" {
			continue
		}
		secret := ""
		if c.SecretRef != "" {
			secret, _ = s.Secrets.Resolve(ctx, c.SecretRef, scriptID)
		}
		if exe, err := buildExecutor("llm", &c, secret); err == nil && exe != nil {
			return exe
		}
	}
	return nil
}

// originGrants resolves the grants of the golden's origin version (best-effort).
func originGrants(ctx context.Context, s *Service, scriptID string, version uint64) []store.GrantRef {
	v, err := s.Store.GetVersion(ctx, scriptID, version)
	if err != nil {
		return nil
	}
	return v.Grants
}

// trajectoryFromLog reconstructs a trajectory from a recorded run's event log (for
// judging an already-completed run, e.g. calibration).
func trajectoryFromLog(events []engine.Event) []eval.TrajectoryStep {
	var out []eval.TrajectoryStep
	for i := range events {
		ev := events[i]
		if ev.Kind != engine.EventRPCResult || ev.RPC == nil {
			continue
		}
		out = append(out, eval.TrajectoryStep{
			Capability: ev.RPC.Capability, Method: ev.RPC.Method,
			Args: ev.RPC.Args, Result: ev.RPC.Result, Err: ev.RPC.Err,
		})
	}
	return out
}

// EvalSuite aggregates N samples of the same eval. Even with recvs fixed and HTTP
// replayed, the live LLM is non-deterministic, so a single replay is not a
// verdict — score over N (pass@k / pass-rate) and surface flakiness, or the eval
// is noisy and loses trust.
type EvalSuite struct {
	GoldenID     string        `json:"golden_id"`
	Version      uint64        `json:"version"`
	K            int           `json:"k"`
	Passes       int           `json:"passes"`
	PassRate     float64       `json:"pass_rate"`
	Flaky        bool          `json:"flaky"`         // some-but-not-all passed: unstable
	MeanCoverage float64       `json:"mean_coverage"` // average trajectory coverage across samples
	Samples      []*EvalResult `json:"samples"`
}

// RunEvalSamples runs k independent samples and aggregates pass-rate + flakiness.
// A sample "passes" by its judge verdict when judging was requested, else by
// completing without error.
func (s *Service) RunEvalSamples(ctx context.Context, req EvalRequest, k int) (*EvalSuite, error) {
	if k < 1 {
		k = 1
	}
	suite := &EvalSuite{K: k}
	var covSum float64
	for i := 0; i < k; i++ {
		res, err := s.RunEval(ctx, req)
		if err != nil {
			return nil, err
		}
		suite.Samples = append(suite.Samples, res)
		suite.GoldenID, suite.Version = res.GoldenID, res.Version
		covSum += res.Coverage
		if samplePassed(res) {
			suite.Passes++
		}
	}
	suite.PassRate = float64(suite.Passes) / float64(k)
	suite.MeanCoverage = covSum / float64(k)
	suite.Flaky = suite.Passes > 0 && suite.Passes < k
	return suite, nil
}

// samplePassed defines per-sample success: the judge's verdict when judging ran,
// otherwise completion without a genuine error.
func samplePassed(r *EvalResult) bool {
	if r.Verdict != nil {
		return r.Verdict.Pass
	}
	return r.Completed && r.Error == ""
}

// recordedRecvs pulls the golden's user-channel answers (inbox.recv results) in
// order — the fixed human side of the conversation the eval replays.
func recordedRecvs(events []engine.Event) []json.RawMessage {
	var out []json.RawMessage
	for i := range events {
		ev := events[i]
		if ev.Kind == engine.EventRPCResult && ev.RPC != nil && ev.RPC.Capability == "inbox" && ev.RPC.Method == "recv" {
			out = append(out, ev.RPC.Result)
		}
	}
	return out
}

// countRPCs counts result events — the golden trajectory length used as the
// coverage denominator.
func countRPCs(events []engine.Event) int {
	n := 0
	for i := range events {
		if events[i].Kind == engine.EventRPCResult && events[i].RPC != nil {
			n++
		}
	}
	return n
}

func coverage(executed, goldenLen int) float64 {
	if goldenLen <= 0 {
		if executed > 0 {
			return 1.0
		}
		return 0
	}
	c := float64(executed) / float64(goldenLen)
	if c > 1.0 {
		c = 1.0
	}
	return c
}

// throwawayInbox accepts send (discarding it) so an eval's inbox.send has no real
// effect; recv is served by the selective mediator, not this executor.
func throwawayInbox() engine.Executor {
	return engine.ExecutorFunc(func(_ context.Context, _ engine.Invocation) (json.RawMessage, error) {
		return json.RawMessage(`null`), nil
	})
}
