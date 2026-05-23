package engine

import (
	"context"
	"encoding/json"
)

// Command is a shell invocation inside a sandbox.
type Command struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env,omitempty"`
	Dir  string            `json:"dir,omitempty"`
}

// ExecResult is the outcome of a sandbox command.
type ExecResult struct {
	Code   int    `json:"code"`
	Stdout []byte `json:"stdout"`
	Stderr []byte `json:"stderr"`
}

// Sandbox is a per-actor isolated environment with a home dir persisted to object
// storage. Prod: kata + Firecracker via k8s RuntimeClass. Dev: local subprocess —
// NOT a security boundary.
type Sandbox interface {
	Exec(ctx context.Context, cmd Command) (ExecResult, error) // backs the "shell" capability
	Snapshot(ctx context.Context) (SnapshotKey, error)         // commits an fs barrier
}

// SandboxPool manages warm templates + home-dir overlays to minimize cold start.
type SandboxPool interface {
	// Acquire boots from the warm template for scriptVersion's image and restores
	// the home dir from restore (nil = fresh).
	Acquire(ctx context.Context, exec ExecutionID, image string, restore *SnapshotKey) (Sandbox, error)
	// Release tears down; if persist, snapshots the home dir first (graceful path).
	Release(ctx context.Context, sb Sandbox, persist bool) error
}

// Invocation is one call the script makes into a capability — the unit that is
// memoized. Its flags drive durability and fs-barrier policy.
type Invocation struct {
	Capability string          `json:"capability"`
	Method     string          `json:"method"`
	Args       json.RawMessage `json:"args"`

	// Idempotent: re-executing is safe. Non-idempotent calls get a write-ahead
	// intent record (durable) before execution.
	Idempotent bool `json:"idempotent"`
	// MutatesFS: writes the actor home dir, forcing an fs snapshot barrier when
	// the result commits.
	MutatesFS bool `json:"mutates_fs"`

	// IdemKey is set by the Mediator for non-idempotent calls before Execute. It
	// is stable across replay/retry of the same call site, so executors with
	// external side effects (e.g. consuming an inbox message) can dedupe.
	IdemKey string `json:"-"`
}

// Executor performs a real (non-replayed) invocation against a bound tool. Called
// only on a log miss; secrets are closed over here in Go and never reach the VM.
// Implementations own their own retry/timeout policy.
type Executor interface {
	Execute(ctx context.Context, inv Invocation) (json.RawMessage, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, inv Invocation) (json.RawMessage, error)

func (f ExecutorFunc) Execute(ctx context.Context, inv Invocation) (json.RawMessage, error) {
	return f(ctx, inv)
}

// Environment is the set of capabilities granted to an execution, assembled from
// the principal's grants. Secret-refs are already resolved into these executors.
type Environment map[string]Executor // capability name -> bound executor

// shellExecutor adapts a Sandbox to the "shell" capability. It is supplied by the
// Mediator (which holds the per-execution Sandbox) rather than the Environment,
// since the sandbox is provisioned after grants are resolved.
type shellExecutor struct{ sb Sandbox }

func (s shellExecutor) Execute(ctx context.Context, inv Invocation) (json.RawMessage, error) {
	var a struct {
		Argv []string          `json:"argv"`
		Dir  string            `json:"dir"`
		Env  map[string]string `json:"env"`
	}
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return nil, err
	}
	res, err := s.sb.Exec(ctx, Command{Argv: a.Argv, Dir: a.Dir, Env: a.Env})
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"code":   res.Code,
		"stdout": string(res.Stdout),
		"stderr": string(res.Stderr),
	})
}
