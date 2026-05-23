package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// countingExec records how many times each capability actually executed.
type countingExec struct {
	mu    sync.Mutex
	calls int
	fn    func(inv Invocation) (json.RawMessage, error)
}

func (c *countingExec) Execute(_ context.Context, inv Invocation) (json.RawMessage, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.fn != nil {
		return c.fn(inv)
	}
	return json.RawMessage(`"ok"`), nil
}

func (c *countingExec) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newHarness(t *testing.T, exec *countingExec) (*MemLeaser, *MemLog, Environment) {
	t.Helper()
	ls := NewMemLeaser()
	log := NewMemLog(ls)
	env := Environment{"test": exec}
	return ls, log, env
}

func inv(method, args string) Invocation {
	return Invocation{Capability: "test", Method: method, Args: json.RawMessage(args), Idempotent: true}
}

func TestMemoMissThenHitWithinRun(t *testing.T) {
	ce := &countingExec{}
	ls, log, env := newHarness(t, ce)
	lease, _ := ls.Acquire(context.Background(), "e1")

	m := NewMediator("e1", log, lease, env, nil, nil)
	r1, err := m.Call(context.Background(), inv("get", `1`))
	if err != nil {
		t.Fatal(err)
	}
	if string(r1) != `"ok"` {
		t.Fatalf("got %s", r1)
	}
	// A *different* call key (next sequential call) executes again.
	if _, err := m.Call(context.Background(), inv("get", `2`)); err != nil {
		t.Fatal(err)
	}
	if ce.count() != 2 {
		t.Fatalf("expected 2 executions, got %d", ce.count())
	}
}

func TestReplaySkipsExecution(t *testing.T) {
	ce := &countingExec{}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()

	// First run: two calls execute and are recorded.
	lease, _ := ls.Acquire(ctx, "e1")
	m := NewMediator("e1", log, lease, env, nil, nil)
	mustCall(t, m, inv("get", `1`))
	mustCall(t, m, inv("get", `2`))
	if ce.count() != 2 {
		t.Fatalf("first run: expected 2 exec, got %d", ce.count())
	}

	// Replay: rebuild from the log, same calls in the same order => all memo hits.
	events, _ := log.Read(ctx, "e1", 0)
	lease2, _ := ls.Acquire(ctx, "e1")
	m2 := NewMediator("e1", log, lease2, env, nil, events)
	r1 := mustCall(t, m2, inv("get", `1`))
	r2 := mustCall(t, m2, inv("get", `2`))
	if ce.count() != 2 {
		t.Fatalf("replay must not execute; got %d total", ce.count())
	}
	if string(r1) != `"ok"` || string(r2) != `"ok"` {
		t.Fatalf("replay returned wrong values: %s %s", r1, r2)
	}
}

func TestNonDeterministicReplayDetected(t *testing.T) {
	ce := &countingExec{}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()

	lease, _ := ls.Acquire(ctx, "e1")
	m := NewMediator("e1", log, lease, env, nil, nil)
	mustCall(t, m, inv("get", `1`))

	events, _ := log.Read(ctx, "e1", 0)
	lease2, _ := ls.Acquire(ctx, "e1")
	m2 := NewMediator("e1", log, lease2, env, nil, events)
	// Same call position, different args => drift.
	_, err := m2.Call(ctx, inv("get", `999`))
	if !errors.Is(err, ErrNonDeterministic) {
		t.Fatalf("expected ErrNonDeterministic, got %v", err)
	}
}

func TestMemoizedErrorReplays(t *testing.T) {
	ce := &countingExec{fn: func(inv Invocation) (json.RawMessage, error) {
		return nil, errors.New("boom")
	}}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()

	lease, _ := ls.Acquire(ctx, "e1")
	m := NewMediator("e1", log, lease, env, nil, nil)
	_, err := m.Call(ctx, inv("get", `1`))
	var ce1 *CallError
	if !errors.As(err, &ce1) {
		t.Fatalf("expected CallError, got %v", err)
	}

	events, _ := log.Read(ctx, "e1", 0)
	lease2, _ := ls.Acquire(ctx, "e1")
	m2 := NewMediator("e1", log, lease2, env, nil, events)
	_, err2 := m2.Call(ctx, inv("get", `1`))
	if !errors.As(err2, &ce1) {
		t.Fatalf("replay expected CallError, got %v", err2)
	}
	if ce.count() != 1 {
		t.Fatalf("error must be memoized; executed %d times", ce.count())
	}
}

func TestNonIdempotentWritesIntent(t *testing.T) {
	ce := &countingExec{}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()
	lease, _ := ls.Acquire(ctx, "e1")
	m := NewMediator("e1", log, lease, env, nil, nil)

	i := inv("post", `1`)
	i.Idempotent = false
	mustCall(t, m, i)

	events, _ := log.Read(ctx, "e1", 0)
	if len(events) != 2 {
		t.Fatalf("expected intent+result = 2 events, got %d", len(events))
	}
	if events[0].Kind != EventRPCIntent || events[1].Kind != EventRPCResult {
		t.Fatalf("expected [intent, result], got [%s, %s]", events[0].Kind, events[1].Kind)
	}
	if events[0].RPC.IdemKey == "" {
		t.Fatal("intent must carry an idempotency key")
	}
}

// snapSandbox counts snapshots for fs-barrier testing.
type snapSandbox struct{ snaps int32 }

func (s *snapSandbox) Exec(context.Context, Command) (ExecResult, error) { return ExecResult{}, nil }
func (s *snapSandbox) Snapshot(context.Context) (SnapshotKey, error) {
	n := atomic.AddInt32(&s.snaps, 1)
	return SnapshotKey(fmt.Sprintf("snap-%d", n)), nil
}

// With debounce=0, an fs-mutating call snapshots immediately (strict per-RPC).
func TestFSBarrierImmediateWhenDebounceZero(t *testing.T) {
	ce := &countingExec{}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()
	sb := &snapSandbox{}
	lease, _ := ls.Acquire(ctx, "e1")
	m := NewMediator("e1", log, lease, env, sb, nil, WithDebounce(0))

	i := inv("write", `1`)
	i.Idempotent = false
	i.MutatesFS = true
	mustCall(t, m, i)

	events, _ := log.Read(ctx, "e1", 0)
	if len(events) != 3 {
		t.Fatalf("expected intent+result+barrier = 3, got %d", len(events))
	}
	if events[2].Kind != EventFSBarrier || events[2].Snapshot == nil {
		t.Fatalf("expected fs barrier with snapshot, got %s", events[2].Kind)
	}
	if atomic.LoadInt32(&sb.snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", sb.snaps)
	}
}

// With a debounce window, fs-mutating calls within the window do NOT snapshot;
// FlushFS at teardown records exactly one barrier for all of them.
func TestFSBarrierDebouncedUntilFlush(t *testing.T) {
	ce := &countingExec{}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()
	sb := &snapSandbox{}
	lease, _ := ls.Acquire(ctx, "e1")
	m := NewMediator("e1", log, lease, env, sb, nil) // default 60s debounce

	for n := 0; n < 3; n++ {
		i := inv("write", `1`)
		i.Idempotent = false
		i.MutatesFS = true
		mustCall(t, m, i)
	}
	// No barrier yet — all three mutations are within the debounce window.
	if atomic.LoadInt32(&sb.snaps) != 0 {
		t.Fatalf("expected 0 snapshots before flush, got %d", sb.snaps)
	}
	events, _ := log.Read(ctx, "e1", 0)
	for _, ev := range events {
		if ev.Kind == EventFSBarrier {
			t.Fatal("did not expect a barrier before flush")
		}
	}
	// Teardown flush records a single barrier capturing the latest state.
	if err := m.FlushFS(ctx); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&sb.snaps) != 1 {
		t.Fatalf("expected exactly 1 snapshot after flush, got %d", sb.snaps)
	}
	// A second flush is a no-op (nothing dirty).
	if err := m.FlushFS(ctx); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&sb.snaps) != 1 {
		t.Fatalf("flush when clean should be a no-op, got %d snapshots", sb.snaps)
	}
}

func TestFenceRejectsStaleWriter(t *testing.T) {
	ce := &countingExec{}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()

	stale, _ := ls.Acquire(ctx, "e1") // token N
	_, _ = ls.Acquire(ctx, "e1")      // token N+1 steals ownership
	m := NewMediator("e1", log, stale, env, nil, nil)
	_, err := m.Call(ctx, inv("get", `1`))
	if !errors.Is(err, ErrLost) && !errors.Is(err, ErrFenced) {
		t.Fatalf("stale writer must be rejected, got %v", err)
	}
}

func TestParallelBranchesDeterministicKeys(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	ce := &countingExec{fn: func(in Invocation) (json.RawMessage, error) {
		mu.Lock()
		seen[string(in.Args)] = true
		mu.Unlock()
		return in.Args, nil
	}}
	ls, log, env := newHarness(t, ce)
	ctx := context.Background()
	lease, _ := ls.Acquire(ctx, "e1")
	root := NewMediator("e1", log, lease, env, nil, nil)

	// Fan out 8 branches concurrently; each makes one call.
	const n = 8
	fan := root.Child()
	children := make([]Mediator, n)
	for i := 0; i < n; i++ {
		children[i] = fan.Child() // deterministic order, before goroutines
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := children[i].Call(ctx, inv("get", fmt.Sprintf(`%d`, i)))
			if err != nil {
				t.Errorf("branch %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if ce.count() != n {
		t.Fatalf("expected %d executions, got %d", n, ce.count())
	}

	// Replay must reproduce all branches with zero new executions, regardless of
	// the (nondeterministic) completion order in the live run.
	events, _ := log.Read(ctx, "e1", 0)
	lease2, _ := ls.Acquire(ctx, "e1")
	root2 := NewMediator("e1", log, lease2, env, nil, events)
	fan2 := root2.Child()
	for i := 0; i < n; i++ {
		c := fan2.Child()
		r, err := c.Call(ctx, inv("get", fmt.Sprintf(`%d`, i)))
		if err != nil {
			t.Fatalf("replay branch %d: %v", i, err)
		}
		if string(r) != fmt.Sprintf(`%d`, i) {
			t.Fatalf("replay branch %d got %s", i, r)
		}
	}
	if ce.count() != n {
		t.Fatalf("replay must not execute; total %d", ce.count())
	}
}

func mustCall(t *testing.T, m Mediator, i Invocation) json.RawMessage {
	t.Helper()
	r, err := m.Call(context.Background(), i)
	if err != nil {
		t.Fatalf("call %s: %v", i.Method, err)
	}
	return r
}
