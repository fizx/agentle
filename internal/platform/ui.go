package platform

import (
	"context"
	"encoding/json"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// UIMessage is one entry in a UI run's transcript.
type UIMessage struct {
	Role   string `json:"role"`             // assistant | system | user
	Text   string `json:"text"`             //
	Blocks []any  `json:"blocks,omitempty"` // typed blocks (code/table/image)
}

// UI is the dashboard-facing projection of an interactive run, built from the
// durable event log: a panel descriptor (chat or form) plus the transcript.
type UI struct {
	Kind       string      `json:"kind"`             // "chat" | "form" | "" (no UI)
	Title      string      `json:"title,omitempty"`  //
	Intro      string      `json:"intro,omitempty"`  //
	Fields     []any       `json:"fields,omitempty"` // form fields (latest form declare)
	Transcript []UIMessage `json:"transcript"`       //
	Status     int         `json:"status"`           //
	Awaiting   bool        `json:"awaiting"`         // suspended waiting for the user
}

// uiEvent is the shape echoed into a "ui" RPC result.
type uiEvent struct {
	Kind   string `json:"kind"` // declare-kind ("chat"/"form") or "say"
	Title  string `json:"title"`
	Intro  string `json:"intro"`
	Fields []any  `json:"fields"`
	Role   string `json:"role"`
	Text   string `json:"text"`
	Blocks []any  `json:"blocks"`
}

// GetUI projects an execution's event log into its UI state. kind is "" when the
// script declared no UI.
func (s *Service) GetUI(ctx context.Context, execID string) (*UI, error) {
	exe, err := s.Store.GetExecution(ctx, execID)
	if err != nil {
		return nil, err
	}
	events, err := s.Log.Read(ctx, engine.ExecutionID(execID), 0)
	if err != nil {
		return nil, err
	}
	ui := &UI{Transcript: []UIMessage{}, Status: exe.Status, Awaiting: exe.Status == int(engine.StatusSuspended)}
	for _, ev := range events {
		if ev.Kind != engine.EventRPCResult || ev.RPC == nil || len(ev.RPC.Result) == 0 {
			continue
		}
		switch ev.RPC.Capability {
		case "ui":
			var u uiEvent
			if json.Unmarshal(ev.RPC.Result, &u) != nil {
				continue
			}
			switch u.Kind {
			case "chat", "form":
				ui.Kind = u.Kind
				ui.Title, ui.Intro, ui.Fields = u.Title, u.Intro, u.Fields
			case "say":
				ui.Transcript = append(ui.Transcript, UIMessage{Role: u.Role, Text: u.Text, Blocks: u.Blocks})
			}
		case "inbox":
			if ev.RPC.Method != "recv" {
				continue
			}
			// A delivered message = a user turn. Render its text if present.
			var m map[string]any
			if json.Unmarshal(ev.RPC.Result, &m) != nil || m == nil {
				continue
			}
			text, _ := m["text"].(string)
			if text == "" {
				if b, err := json.Marshal(m); err == nil {
					text = string(b)
				}
			}
			ui.Transcript = append(ui.Transcript, UIMessage{Role: "user", Text: text})
		}
	}
	return ui, nil
}

// PostMessage delivers a user message (chat input or form submission) to a run's
// workspace inbox and resumes it if parked.
func (s *Service) PostMessage(ctx context.Context, execID string, data json.RawMessage) error {
	exe, err := s.Store.GetExecution(ctx, execID)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if err := s.Inbox.Enqueue(ctx, exe.ActorID, data); err != nil {
		return err
	}
	// Resume now if it's parked on recv (no-op otherwise; the dispatcher also polls).
	return s.Resume(ctx, execID)
}
