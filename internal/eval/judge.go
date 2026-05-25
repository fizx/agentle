package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// Judging modes. A read-prefix eval stops at the first write-miss, so the task is
// NOT complete — judging it "did the task succeed?" marks every prefix run as
// fail. The mode picks the right question.
const (
	// JudgePrefix asks: did the agent correctly reach the right action/decision up
	// to the cut point? (Used when the eval stopped at a write-miss.)
	JudgePrefix = "prefix"
	// JudgeFull asks: did the run succeed? (Used when the eval ran to completion.)
	JudgeFull = "full"
)

// CriterionResult is one rubric line's outcome with supporting evidence.
type CriterionResult struct {
	Criterion string `json:"criterion"`
	Pass      bool   `json:"pass"`
	Evidence  string `json:"evidence,omitempty"`
}

// Verdict is the judge's structured ruling. A structured verdict (criterion →
// pass/fail → evidence) is stable and comparable across versions; freeform
// verdicts drift.
type Verdict struct {
	Pass      bool              `json:"pass"`
	Mode      string            `json:"mode"`
	Criteria  []CriterionResult `json:"criteria,omitempty"`
	Reasoning string            `json:"reasoning,omitempty"`
	Raw       string            `json:"raw,omitempty"` // raw judge text, for debugging
}

// JudgeInput is everything a judge sees. Unlike the simulator (surface-only), the
// judge is unconstrained — it gets the full trajectory including internals —
// because it evaluates rather than role-plays.
type JudgeInput struct {
	Criteria    string           // the rubric (criteria.md); may be empty
	Mode        string           // JudgePrefix | JudgeFull
	GoldenLabel string           // "success" | "failure" — failure inverts the question
	Trajectory  []TrajectoryStep // what the new version did, in order
	Output      json.RawMessage  // main()'s return value, if it completed
	Completed   bool
	StopKind    string // why it stopped (write_miss | recv_exhausted | budget), if any
}

// Judge renders a verdict on an eval run. LLM-as-judge is the default; custom
// code/script judges implement the same interface for cases where correctness is
// checkable programmatically.
type Judge interface {
	Judge(ctx context.Context, in JudgeInput) (*Verdict, error)
}

// LLMJudge scores with an LLM. The model and prompt are pinned (and temperature
// is 0) so verdicts are stable and the judge can be calibrated against the human
// up/down labels.
type LLMJudge struct {
	LLM   engine.Executor // the "llm" capability executor
	Model string          // pinned judge model (may differ from the agent's)
}

const judgeSystemPrompt = `You are a strict, objective evaluator of an AI agent's run.
You are given a success rubric and a transcript of what the agent did (its full
trajectory, including internal tool calls). Decide whether the run meets the rubric.

Respond with ONLY a JSON object, no prose around it:
{"pass": <bool>, "reasoning": "<one paragraph>", "criteria": [{"criterion": "<text>", "pass": <bool>, "evidence": "<quote or reference>"}]}

Rules:
- Judge ONLY on evidence present in the transcript. Do not assume facts the agent never surfaced.
- Be conservative: if the evidence is missing or ambiguous, that criterion fails.`

func (j *LLMJudge) Judge(ctx context.Context, in JudgeInput) (*Verdict, error) {
	prompt := buildJudgePrompt(in)
	temp := 0.0
	args, _ := json.Marshal(map[string]any{
		"model":       j.Model,
		"temperature": temp,
		"messages": []map[string]any{
			{"role": "system", "content": judgeSystemPrompt},
			{"role": "user", "content": prompt},
		},
	})
	out, err := j.LLM.Execute(ctx, engine.Invocation{Capability: "llm", Method: "call", Args: args})
	if err != nil {
		return nil, fmt.Errorf("judge llm call: %w", err)
	}
	var res struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("judge: decode llm result: %w", err)
	}
	v, err := parseVerdict(res.Content)
	if err != nil {
		return nil, err
	}
	v.Mode = in.Mode
	v.Raw = res.Content
	return v, nil
}

// buildJudgePrompt assembles the fixed judge prompt: the mode-specific question,
// the failure-inversion instruction, the rubric, and the rendered trajectory.
func buildJudgePrompt(in JudgeInput) string {
	var b strings.Builder
	switch in.Mode {
	case JudgeFull:
		b.WriteString("QUESTION: Did this run SUCCEED at the task?\n")
	default: // prefix
		b.WriteString("QUESTION: The run was intentionally cut at the first external write (read-prefix eval), so the task is NOT finished. Judge ONLY whether the agent correctly reached the right action/decision up to the cut point — not whether the overall task completed.\n")
	}
	if in.GoldenLabel == "failure" {
		b.WriteString("\nIMPORTANT: The reference run for this case was a KNOWN FAILURE. This run PASSES if it AVOIDED or FIXED that failure, and FAILS if it repeated it.\n")
	}
	b.WriteString("\nRUBRIC:\n")
	if strings.TrimSpace(in.Criteria) == "" {
		b.WriteString("(no explicit rubric provided — judge whether the run did something reasonable and consistent for the apparent task)\n")
	} else {
		b.WriteString(in.Criteria)
		b.WriteString("\n")
	}
	b.WriteString("\nRUN OUTCOME: ")
	if in.Completed {
		b.WriteString("completed\n")
	} else {
		fmt.Fprintf(&b, "stopped (%s)\n", in.StopKind)
	}
	b.WriteString("\nTRANSCRIPT (in order):\n")
	b.WriteString(RenderTrajectory(in.Trajectory))
	if len(in.Output) > 0 && string(in.Output) != "null" {
		fmt.Fprintf(&b, "\nFINAL OUTPUT: %s\n", clip(string(in.Output), 1000))
	}
	return b.String()
}

// RenderTrajectory turns the recorded steps into a compact, readable transcript
// for the judge: LLM turns show the new input + the assistant reply / tool calls;
// http shows method+url+status; recv shows the user message.
func RenderTrajectory(steps []TrajectoryStep) string {
	var b strings.Builder
	for i, s := range steps {
		switch s.Capability {
		case "llm":
			in := lastMessageText(s.Args)
			reply, tools := llmReply(s.Result)
			fmt.Fprintf(&b, "%d. llm ← %s\n", i+1, clip(in, 400))
			if tools != "" {
				fmt.Fprintf(&b, "   llm → tool_calls: %s\n", clip(tools, 400))
			}
			if reply != "" {
				fmt.Fprintf(&b, "   llm → %s\n", clip(reply, 400))
			}
		case "http":
			fmt.Fprintf(&b, "%d. http.%s %s → %s\n", i+1, s.Method, httpURL(s.Args), httpStatus(s.Result, s.Err))
		case "inbox":
			fmt.Fprintf(&b, "%d. user said: %s\n", i+1, clip(lastMessageText(s.Result), 300))
		case "shell":
			fmt.Fprintf(&b, "%d. shell.%s → %s\n", i+1, s.Method, clip(string(s.Result), 200))
		default:
			fmt.Fprintf(&b, "%d. %s.%s → %s\n", i+1, s.Capability, s.Method, clip(string(s.Result), 200))
		}
	}
	if b.Len() == 0 {
		return "(no steps executed)\n"
	}
	return b.String()
}

// parseVerdict extracts the JSON verdict object from a (possibly chatty) model
// reply by taking the first '{' … last '}' span.
func parseVerdict(content string) (*Verdict, error) {
	start := strings.IndexByte(content, '{')
	end := strings.LastIndexByte(content, '}')
	if start < 0 || end <= start {
		return nil, fmt.Errorf("judge: no JSON object in reply: %s", clip(content, 200))
	}
	var v Verdict
	if err := json.Unmarshal([]byte(content[start:end+1]), &v); err != nil {
		return nil, fmt.Errorf("judge: bad verdict json: %w", err)
	}
	return &v, nil
}

// --- small extractors for trajectory rendering ----------------------------

func lastMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// llm args carry {messages:[{role,content}]}; recv results carry {text:...} or
	// a bare {role,content}. Try messages first, then content/text.
	var withMsgs struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Content string `json:"content"`
		Text    string `json:"text"`
	}
	if json.Unmarshal(raw, &withMsgs) == nil {
		if n := len(withMsgs.Messages); n > 0 {
			return withMsgs.Messages[n-1].Content
		}
		if withMsgs.Content != "" {
			return withMsgs.Content
		}
		if withMsgs.Text != "" {
			return withMsgs.Text
		}
	}
	return strings.Trim(string(raw), `"`)
}

func llmReply(raw json.RawMessage) (content, tools string) {
	var r struct {
		Content   string `json:"content"`
		ToolCalls []struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"tool_calls"`
	}
	_ = json.Unmarshal(raw, &r)
	var ts []string
	for _, tc := range r.ToolCalls {
		args, _ := json.Marshal(tc.Arguments)
		ts = append(ts, tc.Name+string(args))
	}
	return r.Content, strings.Join(ts, ", ")
}

func httpURL(raw json.RawMessage) string {
	var a struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(raw, &a)
	return a.URL
}

func httpStatus(raw json.RawMessage, errMsg string) string {
	if errMsg != "" {
		return "error: " + errMsg
	}
	var r struct {
		Status int `json:"status"`
	}
	_ = json.Unmarshal(raw, &r)
	return fmt.Sprintf("status %d", r.Status)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
