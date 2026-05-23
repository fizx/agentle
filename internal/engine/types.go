// Package engine implements durable script execution via deterministic replay.
//
// A script runs top-to-bottom; every capability call (RPC) is memoized in an
// append-only, per-execution event log keyed by a deterministic call key. On
// resume or crash, we restore filesystem state from the latest snapshot barrier,
// replay the log, and actually perform only the RPCs that have no recorded result.
package engine

import (
	"encoding/json"
	"errors"
)

// ExecutionID identifies a single durable script execution. It is pinned to one
// script Version for its entire life so replay stays deterministic across edits.
type ExecutionID string

// Seq is a monotonic position in an execution's event log. It reflects durable
// append order; memoization is keyed by CallKey, not Seq.
type Seq uint64

// FenceToken is issued with an execution lease. A newer lease invalidates older
// tokens; every Append must present a current token.
type FenceToken uint64

// EventKind discriminates the entries in an execution's log.
type EventKind uint8

const (
	// EventRPCIntent is the write-ahead record for a non-idempotent RPC: we are
	// about to attempt it. Lets recovery know a side effect may have fired.
	EventRPCIntent EventKind = iota
	// EventRPCResult is the memoized result (or error) of an RPC.
	EventRPCResult
	// EventFSBarrier records that the home dir was snapshotted consistently with
	// this seq. Emitted on commit of any fs-mutating RPC.
	EventFSBarrier
)

func (k EventKind) String() string {
	switch k {
	case EventRPCIntent:
		return "intent"
	case EventRPCResult:
		return "result"
	case EventFSBarrier:
		return "barrier"
	default:
		return "unknown"
	}
}

// Event is one entry in an execution's append-only log.
type Event struct {
	Seq      Seq         `json:"seq"`
	Kind     EventKind   `json:"kind"`
	RPC      *RPCRecord  `json:"rpc,omitempty"`      // set for Intent / Result
	Snapshot *FSSnapshot `json:"snapshot,omitempty"` // set for FSBarrier
	WallTime int64       `json:"wall_time"`          // unix nanos; tracing only, never logic input
}

// RPCRecord captures a single capability invocation and (on Result) its outcome.
type RPCRecord struct {
	CallKey    string          `json:"call_key"`   // deterministic position in the call tree
	Capability string          `json:"capability"` // e.g. "http", "llm", "kv", "shell"
	Method     string          `json:"method"`     // operation on the capability
	ArgsHash   string          `json:"args_hash"`  // hash of args; replay mismatch => nondeterminism
	IdemKey    string          `json:"idem_key,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"` // memoized payload (Result events)
	Err        string          `json:"err,omitempty"`    // memoized typed error string (Result events)
}

// SnapshotKey references home-dir state in object storage.
type SnapshotKey string

// FSSnapshot references home-dir state in object storage at a log position.
type FSSnapshot struct {
	Key SnapshotKey `json:"key"`
	At  Seq         `json:"at"` // the log seq this snapshot is consistent with
}

// Status is the lifecycle state of an execution.
type Status uint8

const (
	StatusRunning   Status = iota
	StatusCompleted        // script returned
	StatusFailed           // unrecoverable (incl. nondeterminism)
	StatusSuspended        // awaiting a long RPC or external signal
)

func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusSuspended:
		return "suspended"
	default:
		return "unknown"
	}
}

var (
	// ErrConflict means the expected seq did not match the log's next seq: a
	// stale or duplicate writer.
	ErrConflict = errors.New("engine: append seq conflict")
	// ErrFenced means the presented fence token is no longer the current lease.
	ErrFenced = errors.New("engine: fence token superseded")
	// ErrLost means lease ownership was taken over by a newer holder.
	ErrLost = errors.New("engine: lease lost")
	// ErrNonDeterministic means a replayed call did not match the recorded event:
	// the script is nondeterministic or drifted from its pinned version.
	ErrNonDeterministic = errors.New("engine: nondeterministic replay")
	// ErrSuspend marks a run that yielded at a durable suspension point — e.g.
	// recv() with no message waiting. It is NOT a failure: the engine parks the
	// execution (StatusSuspended) and resumes it by replaying the log once its
	// wake condition is met. A capability signals it by returning a *SuspendError.
	ErrSuspend = errors.New("engine: execution suspended")
)

// Suspension describes why a run parked and what should wake it. Workspace names
// an inbox whose next message resumes the run; WakeAt (unix nanos, 0 = none) is a
// durable timer deadline. Either or both may be set; the resumer wakes the run
// when a message is available OR the deadline passes, whichever comes first.
type Suspension struct {
	Workspace string `json:"workspace"`
	WakeAt    int64  `json:"wake_at"`
}

// SuspendError is returned by a capability Executor to park the execution at a
// durable suspension point. The Mediator records no result for the call, so on
// resume the call re-executes and either makes progress or suspends again.
type SuspendError struct{ Suspension }

func (e *SuspendError) Error() string { return "engine: suspended awaiting " + e.Workspace }

// Is reports SuspendError as an instance of ErrSuspend so errors.Is works through
// wrapping (e.g. the Starlark backtrace).
func (e *SuspendError) Is(target error) bool { return target == ErrSuspend }
