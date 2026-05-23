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

// Inbox returns the "inbox" capability for one workspace: send(workspace, data)
// enqueues into another workspace; recv(timeout) blocks for this workspace's next
// message (the actor "yield" point). recv is crash-safe via the call's IdemKey.
func Inbox(q MessageQueue, workspace string, maxWait time.Duration, poll time.Duration) engine.Executor {
	if maxWait == 0 {
		maxWait = 60 * time.Second
	}
	if poll == 0 {
		poll = 200 * time.Millisecond
	}
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
				TimeoutSeconds float64 `json:"timeout_seconds"`
			}
			_ = json.Unmarshal(inv.Args, &a)
			wait := time.Duration(a.TimeoutSeconds * float64(time.Second))
			if wait <= 0 || wait > maxWait {
				wait = maxWait
			}
			deadline := time.Now().Add(wait)
			for {
				msg, ok, err := q.Claim(ctx, workspace, inv.IdemKey)
				if err != nil {
					return nil, err
				}
				if ok {
					return msg, nil
				}
				if time.Now().After(deadline) {
					return json.RawMessage(`null`), nil // timed out: no message
				}
				select {
				case <-time.After(poll):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		default:
			return json.RawMessage(`null`), nil
		}
	})
}
