package caps

import (
	"context"
	"encoding/json"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// UI returns the always-on "ui" capability backing interactive form/chat scripts.
// It simply echoes the call args into the (memoized) result: the dashboard's UI
// projection reads those results from the durable event log to render the panel —
// declare events become the panel descriptor, say events become transcript
// messages. The actual back-and-forth with the user rides the actor inbox
// (send from the panel, recv in the script), so a UI run is just a durably
// suspending actor with a rendered front end.
func UI() engine.Executor {
	return engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
		if len(inv.Args) == 0 {
			return json.RawMessage(`null`), nil
		}
		return inv.Args, nil
	})
}
