package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// UserSim produces the next user message given what the user has seen. It
// replaces the recorded recv tape so a new version's *actual* questions get
// answered, instead of replaying answers bound to the old version's questions.
type UserSim interface {
	Answer(ctx context.Context, surface string) (json.RawMessage, error)
}

// LLMSim is a persona-seeded, LLM-backed user simulator. Crucially it sees only
// the user-visible surface (persona + transcript-so-far) — never the agent's raw
// internals — so it cannot answer using info the agent never surfaced, which
// would paper over UX failures and inflate the pass rate.
type LLMSim struct {
	LLM     engine.Executor
	Model   string
	Persona Persona
}

func (s *LLMSim) Answer(ctx context.Context, surface string) (json.RawMessage, error) {
	temp := 0.0
	args, _ := json.Marshal(map[string]any{
		"model":       s.Model,
		"temperature": temp,
		"messages": []map[string]any{
			{"role": "system", "content": simSystemPrompt(s.Persona)},
			{"role": "user", "content": "CONVERSATION SO FAR (you are the user; the assistant just spoke last):\n\n" + surface + "\n\nReply with ONLY your next message as the user — short, like a real chat reply. No quotation marks, no narration."},
		},
	})
	out, err := s.LLM.Execute(ctx, engine.Invocation{Capability: "llm", Method: "call", Args: args})
	if err != nil {
		return nil, fmt.Errorf("simulator llm call: %w", err)
	}
	var res struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("simulator: decode llm result: %w", err)
	}
	// recv results in this platform are chat messages shaped {text: ...}.
	return json.Marshal(map[string]string{"text": strings.TrimSpace(res.Content)})
}

// simSystemPrompt encodes the persona knobs into the simulator's instructions.
func simSystemPrompt(p Persona) string {
	var b strings.Builder
	b.WriteString("You are role-playing a USER talking to an AI assistant. Stay in character.\n\nYOUR PERSONA:\n")
	if strings.TrimSpace(p.Prose) == "" {
		b.WriteString("(a typical user of this assistant)\n")
	} else {
		b.WriteString(p.Prose + "\n")
	}
	b.WriteString("\nYou know ONLY what your persona states and what has been said in the conversation. ")
	if p.OnUnknown == UnknownImprovise {
		b.WriteString("If asked something your persona doesn't specify, pick a reasonable answer and stay consistent with it for the rest of the conversation.\n")
	} else {
		b.WriteString("If asked something your persona doesn't specify, say you haven't decided or don't know — do NOT invent a specific answer.\n")
	}
	if p.Style == StyleGoalLocked {
		b.WriteString("Be persistent: if the assistant gives a wrong, incomplete, or off-track answer, push back and keep steering until your goal is actually met.\n")
	} else {
		b.WriteString("Be a realistic, fairly trusting user: you may accept a plausible-sounding answer and move on, even if it is subtly wrong.\n")
	}
	return b.String()
}

// RenderSurface builds the user-visible transcript from the trajectory: assistant
// UI messages and the user's own prior replies, in order. Internal calls (llm,
// http, kv, shell) are excluded so the sim stays surface-only — unless the persona
// is in oracle mode (capability-ceiling measurement), which also exposes the
// agent's internal LLM replies.
func RenderSurface(steps []TrajectoryStep, personaContext string) string {
	var b strings.Builder
	for _, s := range steps {
		switch s.Capability {
		case "ui":
			if txt := uiText(s); txt != "" {
				fmt.Fprintf(&b, "ASSISTANT: %s\n", txt)
			}
		case "inbox":
			if s.Method == "recv" {
				fmt.Fprintf(&b, "USER: %s\n", lastMessageText(s.Result))
			}
		case "llm":
			if personaContext == ContextOracle {
				if c, _ := llmReply(s.Result); c != "" {
					fmt.Fprintf(&b, "[assistant-internal] %s\n", c)
				}
			}
		}
	}
	if b.Len() == 0 {
		return "(the assistant has not said anything yet)"
	}
	return b.String()
}

// uiText extracts the user-visible text from a ui call's args (say text, or a
// form/chat declaration's title+intro).
func uiText(s TrajectoryStep) string {
	var a struct {
		Kind  string `json:"kind"`
		Text  string `json:"text"`
		Title string `json:"title"`
		Intro string `json:"intro"`
	}
	_ = json.Unmarshal(s.Args, &a)
	switch {
	case a.Text != "":
		return a.Text
	case a.Intro != "" || a.Title != "":
		return strings.TrimSpace(a.Title + " " + a.Intro)
	default:
		return ""
	}
}
