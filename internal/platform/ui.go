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

// Panel is one entry in the UI panel stack (a chat or a form). Declares push a
// panel; ui_pop()/ui_clear() and a submitted ui_form() pop. The dashboard renders
// the stack as the base panel (usually a chat) with later panels (forms) modal
// over it.
type Panel struct {
	Kind   string `json:"kind"`             // "chat" | "form"
	Title  string `json:"title,omitempty"`  //
	Intro  string `json:"intro,omitempty"`  //
	Fields []any  `json:"fields,omitempty"` // form fields
}

// ToolBatch is a set of editor tool calls the run has asked the client to
// execute (read_source / apply_edit / run). It's "pending" until the client posts
// back a {tool_results, batch} reply with the same batch id.
type ToolBatch struct {
	Batch string `json:"batch"`
	Calls []any  `json:"calls"` // [{id, name, arguments}]
}

// UI is the dashboard-facing projection of an interactive run, built from the
// durable event log: a stack of panels (chat/form) plus the transcript. The
// top-of-stack panel is also mirrored into Kind/Title/Intro/Fields for callers
// that just need "is there a UI, and what's active".
type UI struct {
	Kind         string      `json:"kind"`                    // top panel kind: "chat" | "form" | "" (no UI)
	Title        string      `json:"title,omitempty"`         // top panel
	Intro        string      `json:"intro,omitempty"`         // top panel
	Fields       []any       `json:"fields,omitempty"`        // top panel form fields
	Panels       []Panel     `json:"panels"`                  // the full stack (bottom → top)
	Transcript   []UIMessage `json:"transcript"`              //
	Status       int         `json:"status"`                  //
	Awaiting     bool        `json:"awaiting"`                // suspended waiting for the user
	PendingTools *ToolBatch  `json:"pending_tools,omitempty"` // tool calls awaiting client execution
}

// uiEvent is the shape echoed into a "ui" RPC result.
type uiEvent struct {
	Kind   string `json:"kind"` // declare-kind ("chat"/"form"), "say", "pop", "clear", or "tools"
	Title  string `json:"title"`
	Intro  string `json:"intro"`
	Fields []any  `json:"fields"`
	Role   string `json:"role"`
	Text   string `json:"text"`
	Blocks []any  `json:"blocks"`
	Batch  string `json:"batch"` // "tools": batch id, matched by the client's tool_results reply
	Calls  []any  `json:"calls"` // "tools": [{id, name, arguments}]
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
	ui := &UI{Panels: []Panel{}, Transcript: []UIMessage{}, Status: exe.Status, Awaiting: exe.Status == int(engine.StatusSuspended)}
	var pendingBatch, answeredBatch string
	var pendingCalls []any
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
				ui.Panels = append(ui.Panels, Panel{Kind: u.Kind, Title: u.Title, Intro: u.Intro, Fields: u.Fields})
			case "pop":
				if n := len(ui.Panels); n > 0 {
					ui.Panels = ui.Panels[:n-1]
				}
			case "clear":
				ui.Panels = ui.Panels[:0]
			case "say":
				ui.Transcript = append(ui.Transcript, UIMessage{Role: u.Role, Text: u.Text, Blocks: u.Blocks})
			case "tools":
				// Editor tool calls the client must execute. Record as a transcript
				// card (persists across reloads) and mark this batch pending.
				pendingBatch, pendingCalls = u.Batch, u.Calls
				ui.Transcript = append(ui.Transcript, UIMessage{
					Role:   "tool",
					Blocks: []any{map[string]any{"type": "tool_calls", "batch": u.Batch, "calls": u.Calls}},
				})
			}
		case "inbox":
			if ev.RPC.Method != "recv" {
				continue
			}
			var m map[string]any
			if json.Unmarshal(ev.RPC.Result, &m) != nil || m == nil {
				continue
			}
			// A tool-results delivery answers a pending batch; it's the client
			// fulfilling tool calls, not a user turn, so keep it out of the transcript.
			if _, ok := m["tool_results"]; ok {
				if b, _ := m["batch"].(string); b != "" {
					answeredBatch = b
				}
				continue
			}
			// Otherwise it's a user turn. Render its text if present, else the JSON.
			text, _ := m["text"].(string)
			if text == "" {
				if b, err := json.Marshal(m); err == nil {
					text = string(b)
				}
			}
			ui.Transcript = append(ui.Transcript, UIMessage{Role: "user", Text: text})
		}
	}
	// A batch is still pending if no tool_results delivery has answered it.
	if pendingBatch != "" && pendingBatch != answeredBatch {
		ui.PendingTools = &ToolBatch{Batch: pendingBatch, Calls: pendingCalls}
	}
	// Mirror the active (top) panel into the flat fields for simple callers.
	if n := len(ui.Panels); n > 0 {
		top := ui.Panels[n-1]
		ui.Kind, ui.Title, ui.Intro, ui.Fields = top.Kind, top.Title, top.Intro, top.Fields
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
