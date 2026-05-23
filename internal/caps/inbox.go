package caps

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// MessageQueue is the durable per-workspace inbox backing the "inbox" capability.
type MessageQueue interface {
	Enqueue(ctx context.Context, workspace string, data json.RawMessage) error
	Claim(ctx context.Context, workspace, idemKey string) (json.RawMessage, bool, error)
}

// Inbox returns the "inbox" capability for one workspace:
//
//   - send(workspace, data) enqueues a message into another workspace.
//   - recv(deadline) is the actor "yield" point. It is non-blocking: if a message
//     is waiting it is claimed (crash-safe via the call's IdemKey) and returned;
//     otherwise the execution durably suspends. The engine parks it and resumes
//     by replay when a message arrives at this workspace or the deadline passes.
//
// deadline is an absolute unix-nanos wake time (0 = wait indefinitely), computed
// by the recv builtin from a memoized now() so it is stable across resumes. Once
// the deadline has passed with an empty inbox, recv returns null (timed out).
func Inbox(q MessageQueue, workspace string) engine.Executor {
	return engine.ExecutorFunc(func(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
		switch inv.Method {
		case "send":
			var a struct {
				Workspace string          `json:"workspace"`
				Data      json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(inv.Args, &a); err != nil {
				return nil, err
			}
			if a.Workspace == "" {
				a.Workspace = workspace // default: self
			}
			if err := q.Enqueue(ctx, a.Workspace, a.Data); err != nil {
				return nil, err
			}
			return json.RawMessage(`null`), nil

		case "recv":
			var a struct {
				Deadline int64 `json:"deadline"` // absolute unix nanos; 0 = no deadline
			}
			_ = json.Unmarshal(inv.Args, &a)
			msg, ok, err := q.Claim(ctx, workspace, inv.IdemKey)
			if err != nil {
				return nil, err
			}
			if ok {
				return msg, nil
			}
			// Empty inbox. If a deadline was set and has already passed, this recv
			// times out (a deterministic null result). Otherwise suspend until a
			// message arrives or the deadline fires.
			if a.Deadline > 0 && time.Now().UnixNano() >= a.Deadline {
				return json.RawMessage(`null`), nil
			}
			return nil, &engine.SuspendError{Suspension: engine.Suspension{Workspace: workspace, WakeAt: a.Deadline}}

		default:
			return json.RawMessage(`null`), nil
		}
	})
}
