package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// ErrStopped marks a run that hit the eval boundary — a write-miss, recv
// exhaustion, or a budget cap — rather than a real script failure. The eval
// runner distinguishes it (errors.Is) from a genuine error so it can score the
// evaluable segment and report coverage instead of marking the run failed.
var ErrStopped = errors.New("eval: run stopped at evaluable-segment boundary")

// StopKind says why the evaluable segment ended.
type StopKind int

const (
	// StopWriteMiss: the new version invented an external write with no recorded
	// response (the dangerous cell), under a fail/flag policy.
	StopWriteMiss StopKind = iota
	// StopRecvExhausted: the run asked for more user input than the golden
	// recorded, with no persona simulator wired (phase 4 lifts this).
	StopRecvExhausted
	// StopBudget: a per-eval guardrail (steps / wall-clock / cost) tripped.
	StopBudget
)

func (k StopKind) String() string {
	switch k {
	case StopWriteMiss:
		return "write_miss"
	case StopRecvExhausted:
		return "recv_exhausted"
	case StopBudget:
		return "budget"
	default:
		return "unknown"
	}
}

// StopError is returned from Call at the evaluable-segment boundary.
type StopError struct {
	Kind   StopKind
	Detail string
}

func (e *StopError) Error() string { return "eval stop (" + e.Kind.String() + "): " + e.Detail }

// Is reports StopError as an ErrStopped so errors.Is works through the VM's error
// wrapping (mirrors engine.SuspendError).
func (e *StopError) Is(target error) bool { return target == ErrStopped }

// Budget caps a single eval run so a divergent or looping version fails closed
// rather than burning unbounded time and money. A zero field means "no cap".
type Budget struct {
	MaxSteps int           // max mediated RPCs
	MaxWall  time.Duration // wall-clock cap from run start
}

// Config assembles a selective mediator. The runner (phase 2) supplies the live
// Env (llm/shell/http executors), the Cassette + recorded Recvs from the golden,
// and the policies.
type Config struct {
	Exec       engine.ExecutionID
	Env        engine.Environment // live executors, keyed by capability
	Policy     map[string]RPCMode // per-kind replay table; nil => DefaultPolicy
	Cassette   *Cassette          // HTTP tape; nil => empty (every http call misses)
	Classify   Classifier         // read/write at a cassette miss; nil => DefaultClassifier
	MissPolicy WriteMissPolicy    // what a write-miss does
	Recvs      []json.RawMessage  // recorded user messages, in order (phase 1 user channel)
	Sim        UserSim            // persona-seeded simulator; when set, replaces the Recvs tape
	SimContext string             // persona context mode (surface | oracle) for surface rendering
	Clock      int64              // pinned unix-nanos for time.now(); 0 => fixed epoch marker
	RandSeed   int64              // seed for pinned rand; deterministic across samples
	Budget     Budget
}

type medState struct {
	exec       engine.ExecutionID
	env        engine.Environment
	policy     map[string]RPCMode
	cassette   *Cassette
	classify   Classifier
	missPolicy WriteMissPolicy
	clock      int64

	sim        UserSim
	simContext string

	mu       sync.Mutex
	rng      *rand.Rand
	recvs    []json.RawMessage
	recvPos  int
	executed int
	steps    int
	stop     *StopError
	budget   Budget
	deadline time.Time
	traj     []TrajectoryStep
}

// TrajectoryStep is one recorded call in the eval run — what the new version did,
// in order. The judge reads these as the evidence for its verdict; the runner
// renders them into a transcript.
type TrajectoryStep struct {
	Capability string          `json:"capability"`
	Method     string          `json:"method"`
	Args       json.RawMessage `json:"args,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Err        string          `json:"err,omitempty"`
}

// Mediator is one node in the eval call tree. It implements engine.Mediator with
// a per-RPC-kind replay policy instead of the engine's all-or-nothing memoization.
type Mediator struct {
	st     *medState
	prefix string
	ctr    int
}

// New builds the root selective mediator from cfg.
func New(cfg Config) *Mediator {
	pol := cfg.Policy
	if pol == nil {
		pol = DefaultPolicy()
	}
	cls := cfg.Classify
	if cls == nil {
		cls = DefaultClassifier()
	}
	clock := cfg.Clock
	if clock == 0 {
		clock = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
	}
	st := &medState{
		exec:       cfg.Exec,
		env:        cfg.Env,
		policy:     pol,
		cassette:   cfg.Cassette,
		classify:   cls,
		missPolicy: cfg.MissPolicy,
		clock:      clock,
		rng:        rand.New(rand.NewSource(cfg.RandSeed)), //nolint:gosec // determinism, not security
		recvs:      cfg.Recvs,
		sim:        cfg.Sim,
		simContext: cfg.SimContext,
		budget:     cfg.Budget,
	}
	if cfg.Budget.MaxWall > 0 {
		st.deadline = time.Now().Add(cfg.Budget.MaxWall)
	}
	return &Mediator{st: st}
}

// Executed reports how many RPCs the eval completed before stopping (the coverage
// numerator; the runner supplies the golden-trajectory denominator).
func (m *Mediator) Executed() int {
	m.st.mu.Lock()
	defer m.st.mu.Unlock()
	return m.st.executed
}

// Stop returns the boundary that ended the evaluable segment, or nil if the run
// finished within budget without a write-miss or recv exhaustion.
func (m *Mediator) Stop() *StopError {
	m.st.mu.Lock()
	defer m.st.mu.Unlock()
	return m.st.stop
}

func (m *Mediator) Child() engine.Mediator {
	return &Mediator{st: m.st, prefix: m.nextKey()}
}

// FlushFS is a no-op: an eval runs in a throwaway sandbox whose state is
// discarded, so there is nothing to snapshot.
func (m *Mediator) FlushFS(context.Context) error { return nil }

func (m *Mediator) nextKey() string {
	idx := m.ctr
	m.ctr++
	if m.prefix == "" {
		return strconv.Itoa(idx)
	}
	return m.prefix + "." + strconv.Itoa(idx)
}

func (m *Mediator) Call(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
	key := m.nextKey()
	inv.IdemKey = string(m.st.exec) + ":" + key

	if err := m.charge(); err != nil {
		return nil, err
	}

	mode := ModeLive
	if v, ok := m.st.policy[inv.Capability]; ok {
		mode = v
	}
	switch mode {
	case ModeCassette:
		return m.http(ctx, inv)
	case ModePinned:
		return m.pinned(inv)
	case ModeSimulate:
		if inv.Capability == "inbox" && inv.Method == "recv" {
			return m.recv(ctx)
		}
		return m.live(ctx, inv) // inbox.send and any other simulate-kind method
	default:
		return m.live(ctx, inv)
	}
}

// charge advances the step/wall-clock guardrails, failing closed when a cap trips.
func (m *Mediator) charge() error {
	m.st.mu.Lock()
	defer m.st.mu.Unlock()
	if m.st.stop != nil {
		return m.st.stop // already stopped; surface the same boundary
	}
	m.st.steps++
	if m.st.budget.MaxSteps > 0 && m.st.steps > m.st.budget.MaxSteps {
		return m.setStopLocked(StopBudget, fmt.Sprintf("exceeded %d steps", m.st.budget.MaxSteps))
	}
	if !m.st.deadline.IsZero() && time.Now().After(m.st.deadline) {
		return m.setStopLocked(StopBudget, "exceeded wall-clock cap")
	}
	return nil
}

// live executes the real bound executor (llm / shell / local caps), recording it
// in the trajectory. Executor errors surface as engine.CallError, identical to a
// normal run.
func (m *Mediator) live(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
	exec, ok := m.st.env[inv.Capability]
	if !ok {
		return nil, &engine.CallError{Capability: inv.Capability, Method: inv.Method, Msg: engine.ErrNotGranted.Error()}
	}
	res, err := exec.Execute(ctx, inv)
	if err != nil {
		return nil, &engine.CallError{Capability: inv.Capability, Method: inv.Method, Msg: err.Error()}
	}
	m.record(inv, res, "")
	return res, nil
}

// pinned serves deterministic clock/random values so they don't add noise to
// judge comparisons across samples.
func (m *Mediator) pinned(inv engine.Invocation) (json.RawMessage, error) {
	var res json.RawMessage
	switch {
	case inv.Capability == "time" && inv.Method == "now":
		res, _ = json.Marshal(m.st.clock)
	case inv.Capability == "rand":
		m.st.mu.Lock()
		if m.st.stop != nil {
			st := m.st.stop
			m.st.mu.Unlock()
			return nil, st
		}
		if inv.Method == "int" {
			var a struct {
				N int64 `json:"n"`
			}
			_ = json.Unmarshal(inv.Args, &a)
			if a.N <= 0 {
				res, _ = json.Marshal(0)
			} else {
				res, _ = json.Marshal(m.st.rng.Int63n(a.N))
			}
		} else {
			res, _ = json.Marshal(m.st.rng.Float64())
		}
		m.st.mu.Unlock()
	default: // time.sleep (skip the wait; eval must not block) and any other pin
		res = json.RawMessage(`null`)
	}
	m.record(inv, res, "")
	return res, nil
}

// recv serves the user channel. With a persona simulator wired (phase 4) it asks
// the sim to answer the new version's *actual* question from the user-visible
// surface; otherwise it replays the golden's recorded answers in order, and ends
// the segment when the run asks for more input than was recorded (a cache miss on
// the user channel).
func (m *Mediator) recv(ctx context.Context) (json.RawMessage, error) {
	m.st.mu.Lock()
	if m.st.stop != nil {
		st := m.st.stop
		m.st.mu.Unlock()
		return nil, st
	}

	if m.st.sim != nil {
		surface := RenderSurface(m.st.traj, m.st.simContext)
		m.st.mu.Unlock()
		msg, err := m.st.sim.Answer(ctx, surface)
		if err != nil {
			m.st.mu.Lock()
			st := m.setStopLocked(StopRecvExhausted, "simulator failed: "+err.Error())
			m.st.mu.Unlock()
			return nil, st
		}
		m.record(engine.Invocation{Capability: "inbox", Method: "recv"}, msg, "")
		return msg, nil
	}

	if m.st.recvPos >= len(m.st.recvs) {
		st := m.setStopLocked(StopRecvExhausted,
			fmt.Sprintf("run requested recv #%d but golden recorded only %d", m.st.recvPos+1, len(m.st.recvs)))
		m.st.mu.Unlock()
		return nil, st
	}
	msg := m.st.recvs[m.st.recvPos]
	m.st.recvPos++
	m.st.mu.Unlock()
	m.record(engine.Invocation{Capability: "inbox", Method: "recv"}, msg, "")
	return msg, nil
}

// http applies the cassette: replay on hit; on a miss, classify and either go
// live (read) or apply the write-miss policy.
func (m *Mediator) http(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
	var a httpArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return nil, &engine.CallError{Capability: "http", Method: inv.Method, Msg: "bad http args: " + err.Error()}
	}

	m.st.mu.Lock()
	if m.st.stop != nil {
		st := m.st.stop
		m.st.mu.Unlock()
		return nil, st
	}
	var entry *Entry
	hit := false
	if m.st.cassette != nil {
		entry, hit = m.st.cassette.Lookup(inv.Method, a.URL, a.Body)
	}
	m.st.mu.Unlock()

	if hit {
		// A write-hit is safe: serve the recorded response, no real mutation.
		if entry.Err != "" {
			m.record(inv, nil, entry.Err)
			return nil, &engine.CallError{Capability: "http", Method: inv.Method, Msg: entry.Err}
		}
		m.record(inv, entry.Result, "")
		return entry.Result, nil
	}

	// Miss. The read/write tag decides whether this novel call may run unattended.
	if m.st.classify.IsWrite(inv.Method, inv.Args) {
		switch m.st.missPolicy {
		case MissGoLive:
			return m.live(ctx, inv) // explicitly allowed (sandbox/staging target)
		default: // MissFail | MissFlag
			m.st.mu.Lock()
			defer m.st.mu.Unlock()
			detail := fmt.Sprintf("%s %s has no recorded response", inv.Method, a.URL)
			if m.st.missPolicy == MissFlag {
				detail = "flagged for human: " + detail
			}
			return nil, m.setStopLocked(StopWriteMiss, detail)
		}
	}
	// Read-miss: safe to go live (costs $, no mutation).
	return m.live(ctx, inv)
}

// record logs one completed RPC: it counts toward coverage and appends a step to
// the trajectory the judge later reads.
func (m *Mediator) record(inv engine.Invocation, result json.RawMessage, errMsg string) {
	m.st.mu.Lock()
	m.st.executed++
	m.st.traj = append(m.st.traj, TrajectoryStep{
		Capability: inv.Capability, Method: inv.Method, Args: inv.Args, Result: result, Err: errMsg,
	})
	m.st.mu.Unlock()
}

// Trajectory returns the ordered steps the eval executed — the evidence the judge
// scores. Safe to call after the run returns.
func (m *Mediator) Trajectory() []TrajectoryStep {
	m.st.mu.Lock()
	defer m.st.mu.Unlock()
	return m.st.traj
}

// setStopLocked records the first terminal boundary and returns it. Caller holds
// st.mu.
func (m *Mediator) setStopLocked(kind StopKind, detail string) *StopError {
	if m.st.stop == nil {
		m.st.stop = &StopError{Kind: kind, Detail: detail}
	}
	return m.st.stop
}
