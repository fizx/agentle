package caps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// LLMConfig is a bound llm tool instance. An empty BaseURL or APIKey selects the
// offline mock provider so the platform is playable without credentials.
type LLMConfig struct {
	BaseURL string // OpenAI-compatible base, e.g. https://api.openai.com/v1
	APIKey  string // secret; injected as Bearer, never visible to the script
	Model   string // default model when the script omits one
	Timeout time.Duration
	Client  *http.Client // optional; defaults to a plain client
}

// LLM returns the "llm" capability executor (OpenAI chat-completions format).
func LLM(cfg LLMConfig) engine.Executor {
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.Timeout}
	}
	mock := cfg.BaseURL == "" || cfg.APIKey == ""
	return engine.ExecutorFunc(func(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
		var a struct {
			Messages    []map[string]any `json:"messages"`
			Model       string           `json:"model"`
			Temperature float64          `json:"temperature"`
		}
		if err := json.Unmarshal(inv.Args, &a); err != nil {
			return nil, err
		}
		if mock {
			return mockChat(a.Messages)
		}
		model := a.Model
		if model == "" {
			model = cfg.Model
		}
		reqBody, _ := json.Marshal(map[string]any{
			"model":       model,
			"messages":    a.Messages,
			"temperature": a.Temperature,
		})
		url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

		resp, err := cfg.Client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("llm: upstream status %d: %s", resp.StatusCode, truncate(string(data), 300))
		}
		var parsed struct {
			Choices []struct {
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("llm: decode response: %w", err)
		}
		if len(parsed.Choices) == 0 {
			return nil, fmt.Errorf("llm: empty response")
		}
		return json.Marshal(map[string]any{
			"role":    parsed.Choices[0].Message.Role,
			"content": parsed.Choices[0].Message.Content,
			"usage":   parsed.Usage,
		})
	})
}

func mockChat(messages []map[string]any) (json.RawMessage, error) {
	var last string
	for _, m := range messages {
		if c, ok := m["content"].(string); ok {
			last = c
		}
	}
	content := "[mock] " + truncate(last, 200)
	if last == "" {
		content = "[mock] (no user content)"
	}
	return json.Marshal(map[string]any{
		"role":    "assistant",
		"content": content,
		"usage":   map[string]any{"mock": true},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
