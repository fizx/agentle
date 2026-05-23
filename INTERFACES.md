# Core engine interfaces (sketch)

A first cut at the Go contracts behind the design decisions in PLAN.md. This is a
sketch to pressure-test the model, not wired-up code. The crux is `Mediator.Call`:
it is where the memoization contract, the CAS/fencing rule, and the fs barrier all
meet — if those hold together here, the engine holds together.

```go
package engine

import (
	"context"
	"encoding/json"
)

// ---------------------------------------------------------------------------
// Identifiers
// ---------------------------------------------------------------------------

// ExecutionID identifies a single durable script execution. It is pinned to one
// script Version for its entire life so replay stays deterministic across edits.
type ExecutionID string

// Seq is a monotonic position in an execution's event log.
type Seq uint64

// FenceToken is issued with an execution lease. A newer lease invalidates older
// tokens; every Append must present a current token.
type FenceToken uint64

// ---------------------------------------------------------------------------
// Event log
// ---------------------------------------------------------------------------

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

// Event is one entry in an execution's append-only log.
type Event struct {
	Seq      Seq
	Kind     EventKind
	RPC      *RPCRecord  // set for Intent / Result
	Snapshot *FSSnapshot // set for FSBarrier
	WallTime int64       // for tracing only — never an input to logic
}

// RPCRecord captures a single capability invocation and (on Result) its outcome.
type RPCRecord struct {
	Capability string          // e.g. "http", "llm", "kv", "shell"
	Method     string          // operation on the capability
	ArgsHash   string          // hash of args; replay mismatch => nondeterminism error
	IdemKey    string          // idempotency key (non-idempotent calls only)
	Result     json.RawMessage // memoized payload (Result events)
	Err        string          // memoized typed error string (Result events)
}

// FSSnapshot references home-dir state in object storage at a log position.
type FSSnapshot struct {
	Key SnapshotKey
	At  Seq // the log seq this snapshot is consistent with
}

// Log is the durable, ordered, single-writer-per-execution event store.
// Backends: Redis Cluster + AOF (default), Postgres (durable tier), memory (tests).
type Log interface {
	// Append atomically writes ev at expectedSeq for exec, fenced by token.
	//   - ErrConflict if expectedSeq != the actual next seq (stale/duplicate writer)
	//   - ErrFenced   if token is no longer the current lease token
	// If durable is true, returns only after the event is durably persisted
	// (fsync / quorum); otherwise may return after a best-effort write. Callers
	// pass durable=true for RPC intents and fs barriers, false for idempotent results.
	Append(ctx context.Context, exec ExecutionID, expectedSeq Seq, token FenceToken, ev Event, durable bool) (Seq, error)

	// Read returns events for exec from fromSeq (inclusive), in order.
	Read(ctx context.Context, exec ExecutionID, fromSeq Seq) ([]Event, error)
}

// ---------------------------------------------------------------------------
// Single-writer lease (fencing)
// ---------------------------------------------------------------------------

// Leaser hands out exclusive ownership of an execution.
type Leaser interface {
	Acquire(ctx context.Context, exec ExecutionID) (Lease, error)
}

// Lease is the single-writer right over one execution. Its token must accompany
// every Append; ownership can be stolen on failover, invalidating the token.
type Lease interface {
	Token() FenceToken
	Renew(ctx context.Context) error // ErrLost if ownership was taken over
	Release(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// Capabilities (bound tool instances) and invocations
// ---------------------------------------------------------------------------

// Invocation is one call the script makes into a capability — the unit that is
// memoized. Its flags drive durability and fs-barrier policy.
type Invocation struct {
	Capability string
	Method     string
	Args       json.RawMessage

	// Idempotent: re-executing is safe. Non-idempotent calls get a write-ahead
	// intent record (durable) before execution.
	Idempotent bool
	// MutatesFS: writes the actor home dir, forcing an fs snapshot barrier when
	// the result commits.
	MutatesFS bool
}

// Executor performs a real (non-replayed) invocation against a bound tool. Called
// only on a log miss; secrets are closed over here in Go and never reach the VM.
// Implementations own their own retry/timeout policy.
type Executor interface {
	Execute(ctx context.Context, inv Invocation) (json.RawMessage, error)
}

// Environment is the set of capabilities granted to an execution, assembled from
// the principal's grants. Secret-refs are already resolved into these executors.
type Environment map[string]Executor // capability name -> bound executor

// ---------------------------------------------------------------------------
// Mediator: the memoization contract (the crux)
// ---------------------------------------------------------------------------

// Mediator is what every capability builtin calls to perform an RPC. It enforces:
//
//   replay (log has a Result at the cursor):
//       verify (Capability, Method, ArgsHash) matches the recorded call;
//       mismatch => ErrNonDeterministic. Otherwise return the memoized result.
//
//   miss:
//       if !inv.Idempotent: Append(EventRPCIntent, durable=true)   // write-ahead
//       res := Environment[inv.Capability].Execute(ctx, inv)       // retries/timeout
//       Append(EventRPCResult{res}, durable = !inv.Idempotent)
//       if inv.MutatesFS:
//           key := Sandbox.Snapshot(ctx)
//           Append(EventFSBarrier{key, cursor}, durable=true)
//
// The Mediator owns the seq cursor, the Lease token, the Log, the Environment,
// and the Sandbox handle for one running execution.
type Mediator interface {
	Call(ctx context.Context, inv Invocation) (json.RawMessage, error)
}

// ---------------------------------------------------------------------------
// Engine: drives an execution
// ---------------------------------------------------------------------------

type Status uint8

const (
	StatusRunning   Status = iota
	StatusCompleted        // script returned
	StatusFailed           // unrecoverable (incl. nondeterminism)
	StatusSuspended        // awaiting a long RPC or external signal
)

// Engine runs a single execution to completion or to its next durable suspension
// point. It acquires the lease, restores fs from the latest barrier, replays the
// log, then continues. Idempotent: safe to call again for crash recovery / resume.
type Engine interface {
	Run(ctx context.Context, exec ExecutionID) (Status, error)
}

// ---------------------------------------------------------------------------
// Sandbox
// ---------------------------------------------------------------------------

type Command struct {
	Argv []string
	Env  map[string]string
	Dir  string
}

type ExecResult struct {
	Code   int
	Stdout []byte
	Stderr []byte
}

// SnapshotKey references home-dir state in object storage.
type SnapshotKey string

// Sandbox is a per-actor isolated environment with a home dir persisted to object
// storage. Prod: kata + Firecracker via k8s RuntimeClass. Dev: local — NOT a
// security boundary.
type Sandbox interface {
	Exec(ctx context.Context, cmd Command) (ExecResult, error) // backs the "shell" capability
	Snapshot(ctx context.Context) (SnapshotKey, error)         // commits an fs barrier
}

// SandboxPool manages warm templates + home-dir overlays to minimize cold start.
type SandboxPool interface {
	// Acquire boots from the warm template for scriptVersion's image and restores
	// the home dir from restore (nil = fresh).
	Acquire(ctx context.Context, exec ExecutionID, scriptVersion string, restore *SnapshotKey) (Sandbox, error)
	// Release tears down; if persist, snapshots the home dir first (graceful path).
	Release(ctx context.Context, sb Sandbox, persist bool) error
}

// ---------------------------------------------------------------------------
// Scripts & grants
// ---------------------------------------------------------------------------

// Version is an immutable snapshot of a script. Executions pin to one Version.
type Version struct {
	ScriptID string
	Version  uint64
	Source   string     // Starlark source
	Image    string     // sandbox template id (language runtime versions)
	Grants   []GrantRef // which configured tool instances this version may use
}

// GrantRef names a configured tool instance available to a principal/script. The
// referenced ToolConfig (endpoint + secret-ref + limits) is resolved into an
// Executor when the Environment is assembled.
type GrantRef struct {
	Capability string // the name the script sees, e.g. "http"
	ConfigID   string // -> ToolConfig (stored separately, holds the secret-ref)
}
```

## Deferred (not yet specified here)
- Triggers (cron, webhook) — the actors that *create* executions.
- KV store capability semantics (durability tier).
- Dashboard / control-plane API.
- OTel exporter wiring (spans derive from log events).
