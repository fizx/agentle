package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultSnapshotDebounce bounds how often a long-running execution snapshots its
// home dir to object storage: at most once per interval while dirty, plus a final
// snapshot on teardown (FlushFS). Trades fine-grained crash recovery for far fewer
// object-storage writes — recovery replays from the last barrier and re-executes
// any fs-mutating RPCs after it (which must be idempotent or self-barriering).
const defaultSnapshotDebounce = 60 * time.Second

// CallError is a memoized capability failure, surfaced to the script identically
// on first execution and on replay.
type CallError struct {
	Capability string
	Method     string
	Msg        string
}

func (e *CallError) Error() string {
	return fmt.Sprintf("%s.%s: %s", e.Capability, e.Method, e.Msg)
}

// Is lets errors.Is(err, ErrNotGranted) match a not-granted CallError, so callers
// can branch on it without string matching.
func (e *CallError) Is(target error) bool {
	return target == ErrNotGranted && e.Msg == ErrNotGranted.Error()
}

// Mediator is what every capability builtin calls to perform an RPC. It enforces
// the memoization contract: a recorded Result is verified against the call and
// returned without re-executing; a miss executes the bound Executor and records
// the outcome (write-ahead intent for non-idempotent calls, fs barrier for
// fs-mutating calls).
//
// Memoization is keyed by a deterministic CallKey — the call's position in the
// execution's call tree — not by log seq. This makes structured concurrency
// (parallel_map) replay-safe: each branch occupies a stable subtree regardless
// of the order in which concurrent calls actually complete.
type Mediator interface {
	Call(ctx context.Context, inv Invocation) (json.RawMessage, error)
	// Child returns a mediator rooted at a fresh, deterministic subtree. Used to
	// give each branch of structured concurrency its own stable call-key space.
	Child() Mediator
	// FlushFS forces a snapshot barrier if the home dir has uncommitted changes.
	// Called on graceful teardown (e.g. before a durable suspension) so the latest
	// home-dir state is recorded in the log for a later resume. No-op if clean or
	// if the execution has no sandbox.
	FlushFS(ctx context.Context) error
}

// medState is shared across all mediator nodes of one execution. Appends and the
// memo/cursor are serialized here; Executors run outside the lock.
type medState struct {
	exec  ExecutionID
	log   Log
	lease Lease
	env   Environment
	sb    Sandbox

	debounce time.Duration // min interval between fs snapshot barriers

	mu       sync.Mutex
	cursor   Seq                  // next seq to append at
	memo     map[string]RPCRecord // callKey -> recorded Result
	fsDirty  bool                 // home dir mutated since the last barrier
	lastSnap time.Time            // when the last barrier was taken
}

// MediatorOption configures a root mediator at construction.
type MediatorOption func(*medState)

// WithDebounce sets the minimum interval between fs snapshot barriers. Zero means
// snapshot on every fs-mutating RPC (the strict, per-RPC crash-safety behavior).
func WithDebounce(d time.Duration) MediatorOption {
	return func(st *medState) { st.debounce = d }
}

// mediator is one node in the call tree: a prefix path plus a local counter.
type mediator struct {
	st     *medState
	prefix string // dotted path of the parent, "" at root
	ctr    int    // next child index at this node (not shared across nodes)
}

// NewMediator builds the root mediator for an execution. existing is the log read
// back for replay; its Result events seed the memo and its length sets the append
// cursor so fresh calls continue after the replayed prefix.
func NewMediator(exec ExecutionID, log Log, lease Lease, env Environment, sb Sandbox, existing []Event, opts ...MediatorOption) Mediator {
	st := &medState{
		exec:     exec,
		log:      log,
		lease:    lease,
		env:      env,
		sb:       sb,
		memo:     make(map[string]RPCRecord),
		debounce: defaultSnapshotDebounce,
		lastSnap: time.Now(),
	}
	for _, o := range opts {
		o(st)
	}
	for _, ev := range existing {
		if ev.Seq+1 > st.cursor {
			st.cursor = ev.Seq + 1
		}
		if ev.Kind == EventRPCResult && ev.RPC != nil {
			st.memo[ev.RPC.CallKey] = *ev.RPC
		}
	}
	return &mediator{st: st}
}

func (m *mediator) Child() Mediator {
	key := m.nextKey()
	return &mediator{st: m.st, prefix: key}
}

// nextKey allocates this node's next deterministic call key.
func (m *mediator) nextKey() string {
	idx := m.ctr
	m.ctr++
	if m.prefix == "" {
		return strconv.Itoa(idx)
	}
	return m.prefix + "." + strconv.Itoa(idx)
}

func (m *mediator) Call(ctx context.Context, inv Invocation) (json.RawMessage, error) {
	key := m.nextKey()
	// Every call gets a stable idempotency key derived from its deterministic
	// call position. Executors that dedupe side effects (non-idempotent RPCs, the
	// inbox claim) use it; it is identical across replay/resume.
	inv.IdemKey = idemKey(m.st.exec, key)
	argsHash := hashArgs(inv)

	// Fast path: memo hit (replay). Verify the recorded call matches.
	m.st.mu.Lock()
	if rec, ok := m.st.memo[key]; ok {
		m.st.mu.Unlock()
		if rec.Capability != inv.Capability || rec.Method != inv.Method || rec.ArgsHash != argsHash {
			return nil, fmt.Errorf("%w: at call %s expected %s.%s/%s, got %s.%s/%s",
				ErrNonDeterministic, key, rec.Capability, rec.Method, rec.ArgsHash,
				inv.Capability, inv.Method, argsHash)
		}
		if rec.Err != "" {
			return nil, &CallError{Capability: inv.Capability, Method: inv.Method, Msg: rec.Err}
		}
		return rec.Result, nil
	}
	m.st.mu.Unlock()

	exec, ok := m.st.env[inv.Capability]
	if !ok && inv.Capability == "shell" && m.st.sb != nil {
		exec, ok = shellExecutor{m.st.sb}, true
	}
	if !ok {
		return nil, &CallError{Capability: inv.Capability, Method: inv.Method, Msg: ErrNotGranted.Error()}
	}

	// Miss. Write-ahead intent for non-idempotent calls so recovery knows a side
	// effect may have fired. The stable IdemKey is handed to the executor so it
	// can dedupe external side effects across replay/retry.
	if !inv.Idempotent {
		intent := Event{Kind: EventRPCIntent, WallTime: time.Now().UnixNano(), RPC: &RPCRecord{
			CallKey: key, Capability: inv.Capability, Method: inv.Method, ArgsHash: argsHash, IdemKey: inv.IdemKey,
		}}
		if err := m.append(ctx, intent, true); err != nil {
			return nil, err
		}
	}

	// Execute the real RPC outside the lock.
	res, callErr := exec.Execute(ctx, inv)

	// Durable suspension: the call cannot make progress yet (e.g. recv with an
	// empty inbox). Record nothing — on resume this call re-executes from the same
	// call key and either succeeds or suspends again — and propagate the signal so
	// the engine parks the execution.
	if errors.Is(callErr, ErrSuspend) {
		return nil, callErr
	}

	rec := &RPCRecord{CallKey: key, Capability: inv.Capability, Method: inv.Method, ArgsHash: argsHash, IdemKey: inv.IdemKey}
	if callErr != nil {
		rec.Err = callErr.Error()
	} else {
		rec.Result = res
	}
	result := Event{Kind: EventRPCResult, WallTime: time.Now().UnixNano(), RPC: rec}
	if err := m.append(ctx, result, !inv.Idempotent); err != nil {
		return nil, err
	}

	// fs-mutating calls mark the home dir dirty; we only snapshot when the debounce
	// interval has elapsed (or on FlushFS at teardown). This keeps object-storage
	// writes bounded for chatty fs workloads.
	if inv.MutatesFS && callErr == nil && m.st.sb != nil {
		m.st.mu.Lock()
		m.st.fsDirty = true
		due := time.Since(m.st.lastSnap) >= m.st.debounce
		m.st.mu.Unlock()
		if due {
			if err := m.snapshotBarrier(ctx); err != nil {
				return nil, err
			}
		}
	}

	if callErr != nil {
		return nil, &CallError{Capability: inv.Capability, Method: inv.Method, Msg: callErr.Error()}
	}
	return res, nil
}

// FlushFS records a final snapshot barrier if the home dir is dirty, so a later
// resume restores the latest state. Called on graceful teardown (suspension).
func (m *mediator) FlushFS(ctx context.Context) error {
	m.st.mu.Lock()
	dirty := m.st.fsDirty && m.st.sb != nil
	m.st.mu.Unlock()
	if !dirty {
		return nil
	}
	return m.snapshotBarrier(ctx)
}

// snapshotBarrier snapshots the home dir and appends an FSBarrier event, then
// clears the dirty flag and resets the debounce clock.
func (m *mediator) snapshotBarrier(ctx context.Context) error {
	skey, err := m.st.sb.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("fs snapshot barrier: %w", err)
	}
	m.st.mu.Lock()
	at := m.st.cursor
	m.st.mu.Unlock()
	barrier := Event{Kind: EventFSBarrier, WallTime: time.Now().UnixNano(), Snapshot: &FSSnapshot{Key: skey, At: at}}
	if err := m.append(ctx, barrier, true); err != nil {
		return err
	}
	m.st.mu.Lock()
	m.st.fsDirty = false
	m.st.lastSnap = time.Now()
	m.st.mu.Unlock()
	return nil
}

// append serializes a write: renew the lease, CAS at the current cursor, record
// the memo entry for Result events. Retries once on benign cursor races from
// concurrent branches.
func (m *mediator) append(ctx context.Context, ev Event, durable bool) error {
	if err := m.st.lease.Renew(ctx); err != nil {
		return err
	}
	m.st.mu.Lock()
	defer m.st.mu.Unlock()
	for {
		seq, err := m.st.log.Append(ctx, m.st.exec, m.st.cursor, m.st.lease.Token(), ev, durable)
		if err == ErrConflict {
			// Another writer advanced the log under us (durable backend shared
			// across processes). Re-sync the cursor and retry.
			events, rerr := m.st.log.Read(ctx, m.st.exec, m.st.cursor)
			if rerr != nil {
				return rerr
			}
			if len(events) == 0 {
				return err
			}
			for _, e := range events {
				if e.Seq+1 > m.st.cursor {
					m.st.cursor = e.Seq + 1
				}
				if e.Kind == EventRPCResult && e.RPC != nil {
					m.st.memo[e.RPC.CallKey] = *e.RPC
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		m.st.cursor = seq + 1
		if ev.Kind == EventRPCResult && ev.RPC != nil {
			m.st.memo[ev.RPC.CallKey] = *ev.RPC
		}
		return nil
	}
}

func hashArgs(inv Invocation) string {
	h := sha256.New()
	h.Write([]byte(inv.Capability))
	h.Write([]byte{0})
	h.Write([]byte(inv.Method))
	h.Write([]byte{0})
	h.Write(inv.Args)
	return hex.EncodeToString(h.Sum(nil))
}

func idemKey(exec ExecutionID, callKey string) string {
	return strings.Join([]string{string(exec), callKey}, ":")
}
