package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// fakeLLM returns a canned chat result whose content is produced by fn(prompt),
// letting tests drive the judge's verdict and assert on the prompt it built.
func fakeLLM(fn func(systemPrompt, userPrompt string) string) engine.Executor {
	return engine.ExecutorFunc(func(_ context.Context, inv engine.Invocation) (json.RawMessage, error) {
		var a struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(inv.Args, &a)
		var sys, usr string
		for _, m := range a.Messages {
			if m.Role == "system" {
				sys = m.Content
			} else {
				usr = m.Content
			}
		}
		return json.Marshal(map[string]any{"role": "assistant", "content": fn(sys, usr)})
	})
}

func sampleTrajectory() []TrajectoryStep {
	llmArgs, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": "what is the capital of Japan?"}}})
	llmRes, _ := json.Marshal(map[string]any{"content": "Tokyo is the capital."})
	return []TrajectoryStep{
		{Capability: "http", Method: "get", Args: mustArgs("https://api/geo", ""), Result: json.RawMessage(`{"status":200,"body":"JP"}`)},
		{Capability: "llm", Method: "call", Args: llmArgs, Result: llmRes},
	}
}

func TestLLMJudgePassParse(t *testing.T) {
	var sawPrompt string
	j := &LLMJudge{Model: "judge-x", LLM: fakeLLM(func(_, usr string) string {
		sawPrompt = usr
		return "Here is my verdict:\n{\"pass\": true, \"reasoning\": \"found Tokyo\", \"criteria\": [{\"criterion\":\"names capital\",\"pass\":true,\"evidence\":\"Tokyo\"}]}"
	})}
	v, err := j.Judge(context.Background(), JudgeInput{
		Criteria: "The agent must name the capital of Japan.", Mode: JudgeFull, GoldenLabel: "success",
		Trajectory: sampleTrajectory(), Completed: true, Output: json.RawMessage(`"Tokyo"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Pass || v.Mode != JudgeFull || len(v.Criteria) != 1 || !v.Criteria[0].Pass {
		t.Fatalf("verdict = %+v", v)
	}
	// The prompt must carry the rubric + a rendered transcript + the full-mode question.
	if !strings.Contains(sawPrompt, "capital of Japan") || !strings.Contains(sawPrompt, "Did this run SUCCEED") {
		t.Fatalf("prompt missing rubric/question: %s", sawPrompt)
	}
	if !strings.Contains(sawPrompt, "Tokyo is the capital") {
		t.Fatalf("prompt missing rendered llm reply: %s", sawPrompt)
	}
}

func TestLLMJudgePrefixModeQuestion(t *testing.T) {
	var sawPrompt string
	j := &LLMJudge{LLM: fakeLLM(func(_, usr string) string {
		sawPrompt = usr
		return `{"pass": true}`
	})}
	_, err := j.Judge(context.Background(), JudgeInput{
		Mode: JudgePrefix, Trajectory: sampleTrajectory(), Completed: false, StopKind: "write_miss",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawPrompt, "read-prefix") || strings.Contains(sawPrompt, "Did this run SUCCEED") {
		t.Fatalf("prefix mode should ask the prefix question, got: %s", sawPrompt)
	}
	if !strings.Contains(sawPrompt, "stopped (write_miss)") {
		t.Fatalf("prompt missing stop outcome: %s", sawPrompt)
	}
}

func TestLLMJudgeFailureInversion(t *testing.T) {
	var sawPrompt string
	j := &LLMJudge{LLM: fakeLLM(func(_, usr string) string {
		sawPrompt = usr
		return `{"pass": true, "reasoning": "avoided the bug"}`
	})}
	_, err := j.Judge(context.Background(), JudgeInput{
		Mode: JudgeFull, GoldenLabel: "failure", Trajectory: sampleTrajectory(), Completed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawPrompt, "KNOWN FAILURE") || !strings.Contains(sawPrompt, "AVOIDED") {
		t.Fatalf("failure golden must invert the question: %s", sawPrompt)
	}
}

func TestLLMJudgeUnparseable(t *testing.T) {
	j := &LLMJudge{LLM: fakeLLM(func(_, _ string) string { return "I cannot decide." })}
	if _, err := j.Judge(context.Background(), JudgeInput{Mode: JudgeFull}); err == nil {
		t.Fatal("expected error on non-JSON judge reply")
	}
}

func TestCalibratePerfectAndKappa(t *testing.T) {
	// Perfect agreement with both classes present => accuracy 1, kappa 1.
	perfect := Calibrate([]LabelPair{
		{true, true}, {true, true}, {false, false}, {false, false},
	})
	if perfect.Accuracy != 1.0 || perfect.Kappa != 1.0 {
		t.Fatalf("perfect = %+v", perfect)
	}

	// Judge always says pass; humans split 50/50 => accuracy .5, kappa 0 (no skill).
	chance := Calibrate([]LabelPair{
		{true, true}, {false, true}, {true, true}, {false, true},
	})
	if chance.Accuracy != 0.5 {
		t.Fatalf("chance accuracy = %v", chance.Accuracy)
	}
	if chance.Kappa != 0 {
		t.Fatalf("chance kappa = %v (want 0; judge has no skill)", chance.Kappa)
	}
	if chance.FP != 2 || chance.TP != 2 {
		t.Fatalf("confusion = %+v", chance)
	}
}

func TestCalibrateConfusionCounts(t *testing.T) {
	st := Calibrate([]LabelPair{
		{true, true},   // TP
		{true, false},  // FN (judge too strict)
		{false, true},  // FP (judge too lenient)
		{false, false}, // TN
	})
	if st.TP != 1 || st.FN != 1 || st.FP != 1 || st.TN != 1 || st.Accuracy != 0.5 {
		t.Fatalf("stats = %+v", st)
	}
}
