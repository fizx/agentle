package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ExecutionSpec is everything needed to run one execution: its pinned source,
// sandbox image, input, and the resolved capability environment.
type ExecutionSpec struct {
	Source string
	Image  string
	Input  json.RawMessage
	Env    Environment
}

// Resolver supplies execution metadata and records terminal state. It is the
// seam between the engine and the data-model store.
type Resolver interface {
	Resolve(ctx context.Context, exec ExecutionID) (ExecutionSpec, error)
	SetStatus(ctx context.Context, exec ExecutionID, status Status, output json.RawMessage, errMsg string) error
	// Suspend parks exec at a durable suspension point, recording what should wake
	// it (a workspace inbox message and/or a deadline). The execution can be
	// resumed later by calling Run again — it replays the log and continues.
	Suspend(ctx context.Context, exec ExecutionID, s Suspension) error
}

// Runner executes a script body, calling the Mediator for every RPC. It must be
// deterministic given the same memoized results — that is the replay invariant.
type Runner interface {
	Run(ctx context.Context, m Mediator, source string, input json.RawMessage) (output json.RawMessage, err error)
}

// Engine runs a single execution to completion or to its next durable suspension
// point. It acquires the lease, restores fs from the latest barrier, replays the
// log, then continues. Idempotent: safe to call again for crash recovery / resume.
type Engine struct {
	Leaser   Leaser
	Log      Log
	Pool     SandboxPool   // optional; nil if the script uses no shell capability
	Runner   Runner
	Res      Resolver
	Debounce time.Duration // min interval between fs snapshot barriers (0 = engine default)
}

// Run drives exec. Errors from Resolve/lease are returned directly; script
// failures are recorded as StatusFailed and returned alongside the status.
func (e *Engine) Run(ctx context.Context, exec ExecutionID) (Status, error) {
	lease, err := e.Leaser.Acquire(ctx, exec)
	if err != nil {
		return StatusFailed, fmt.Errorf("acquire lease: %w", err)
	}
	defer lease.Release(ctx)

	spec, err := e.Res.Resolve(ctx, exec)
	if err != nil {
		return StatusFailed, fmt.Errorf("resolve execution: %w", err)
	}

	events, err := e.Log.Read(ctx, exec, 0)
	if err != nil {
		return StatusFailed, fmt.Errorf("read log: %w", err)
	}

	var sb Sandbox
	if e.Pool != nil {
		restore := latestSnapshot(events)
		sb, err = e.Pool.Acquire(ctx, exec, spec.Image, restore)
		if err != nil {
			return StatusFailed, fmt.Errorf("acquire sandbox: %w", err)
		}
		// The engine records snapshots as log barriers (debounced / on suspend), so
		// the pool need not snapshot again on release.
		defer e.Pool.Release(ctx, sb, false)
	}

	m := NewMediator(exec, e.Log, lease, spec.Env, sb, events, WithDebounce(e.Debounce))

	output, runErr := e.Runner.Run(ctx, m, spec.Source, spec.Input)

	// Durable suspension: park the run instead of failing it. Flush a final fs
	// barrier first so a resume restores the latest home dir.
	if errors.Is(runErr, ErrSuspend) {
		if ferr := m.FlushFS(ctx); ferr != nil {
			_ = e.Res.SetStatus(ctx, exec, StatusFailed, nil, ferr.Error())
			return StatusFailed, fmt.Errorf("flush fs on suspend: %w", ferr)
		}
		var se *SuspendError
		_ = errors.As(runErr, &se)
		var susp Suspension
		if se != nil {
			susp = se.Suspension
		}
		if err := e.Res.Suspend(ctx, exec, susp); err != nil {
			return StatusSuspended, fmt.Errorf("record suspension: %w", err)
		}
		return StatusSuspended, nil
	}
	if runErr != nil {
		_ = e.Res.SetStatus(ctx, exec, StatusFailed, nil, runErr.Error())
		return StatusFailed, runErr
	}
	if err := e.Res.SetStatus(ctx, exec, StatusCompleted, output, ""); err != nil {
		return StatusCompleted, fmt.Errorf("record completion: %w", err)
	}
	return StatusCompleted, nil
}

// latestSnapshot returns the most recent fs-barrier snapshot key, or nil.
func latestSnapshot(events []Event) *SnapshotKey {
	var key *SnapshotKey
	for i := range events {
		if events[i].Kind == EventFSBarrier && events[i].Snapshot != nil {
			k := events[i].Snapshot.Key
			key = &k
		}
	}
	return key
}
