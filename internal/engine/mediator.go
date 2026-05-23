package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

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
}

// medState is shared across all mediator nodes of one execution. Appends and the
// memo/cursor are serialized here; Executors run outside the lock.
type medState struct {
	exec  ExecutionID
	log   Log
	lease Lease
	env   Environment
	sb    Sandbox

	mu     sync.Mutex
	cursor Seq                  // next seq to append at
	memo   map[string]RPCRecord // callKey -> recorded Result
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
func NewMediator(exec ExecutionID, log Log, lease Lease, env Environment, sb Sandbox, existing []Event) Mediator {
	st := &medState{
		exec:  exec,
		log:   log,
		lease: lease,
		env:   env,
		sb:    sb,
		memo:  make(map[string]RPCRecord),
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
	if !ok {
		return nil, &CallError{Capability: inv.Capability, Method: inv.Method, Msg: "capability not granted"}
	}

	// Miss. Write-ahead intent for non-idempotent calls so recovery knows a side
	// effect may have fired.
	if !inv.Idempotent {
		intent := Event{Kind: EventRPCIntent, WallTime: time.Now().UnixNano(), RPC: &RPCRecord{
			CallKey: key, Capability: inv.Capability, Method: inv.Method, ArgsHash: argsHash, IdemKey: idemKey(m.st.exec, key),
		}}
		if err := m.append(ctx, intent, true); err != nil {
			return nil, err
		}
	}

	// Execute the real RPC outside the lock.
	res, callErr := exec.Execute(ctx, inv)

	rec := &RPCRecord{CallKey: key, Capability: inv.Capability, Method: inv.Method, ArgsHash: argsHash}
	if !inv.Idempotent {
		rec.IdemKey = idemKey(m.st.exec, key)
	}
	if callErr != nil {
		rec.Err = callErr.Error()
	} else {
		rec.Result = res
	}
	result := Event{Kind: EventRPCResult, WallTime: time.Now().UnixNano(), RPC: rec}
	if err := m.append(ctx, result, !inv.Idempotent); err != nil {
		return nil, err
	}

	// fs-mutating calls force a snapshot barrier on commit.
	if inv.MutatesFS && callErr == nil && m.st.sb != nil {
		skey, err := m.st.sb.Snapshot(ctx)
		if err != nil {
			return nil, fmt.Errorf("fs snapshot barrier: %w", err)
		}
		m.st.mu.Lock()
		at := m.st.cursor
		m.st.mu.Unlock()
		barrier := Event{Kind: EventFSBarrier, WallTime: time.Now().UnixNano(), Snapshot: &FSSnapshot{Key: skey, At: at}}
		if err := m.append(ctx, barrier, true); err != nil {
			return nil, err
		}
	}

	if callErr != nil {
		return nil, &CallError{Capability: inv.Capability, Method: inv.Method, Msg: callErr.Error()}
	}
	return res, nil
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
