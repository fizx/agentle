package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/kylemaxwell/agentle/internal/store"
)

// seed installs default tool configs (idempotent) and, on an empty database, a
// sample script so the dashboard is immediately playable.
func seed(ctx context.Context, st *store.Store, log *slog.Logger) error {
	// Always-present mock llm config (no credentials needed).
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm", Config: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	// A conservative public http allowlist for demos.
	httpCfg, _ := json.Marshal(map[string]any{"allow": []string{"api.github.com", "*.githubusercontent.com", "httpbin.org"}})
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "http-public", Capability: "http", Config: httpCfg}); err != nil {
		return err
	}

	llmGrant := "llm-mock"
	// If an API key is present in the environment, wire a real OpenAI-compatible config.
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		if err := st.PutSecret(ctx, "OPENAI_API_KEY", key); err != nil {
			return err
		}
		base := envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
		model := envOr("OPENAI_MODEL", "gpt-4o-mini")
		cfg, _ := json.Marshal(map[string]any{"base_url": base, "model": model})
		if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "openai", Capability: "llm", Config: cfg, SecretRef: "OPENAI_API_KEY"}); err != nil {
			return err
		}
		llmGrant = "openai"
		log.Info("configured live llm from OPENAI_API_KEY", "model", model)
	}

	scripts, err := st.ListScripts(ctx)
	if err != nil {
		return err
	}
	if len(scripts) > 0 {
		return nil // don't clobber an existing workspace
	}

	sc, err := st.CreateScript(ctx, "sc_hello", "hello-agent")
	if err != nil {
		return err
	}
	if _, err := st.SaveVersion(ctx, sc.ID, sampleSource, "", []store.GrantRef{{Capability: "llm", ConfigID: llmGrant}}); err != nil {
		return err
	}
	log.Info("seeded sample script", "id", sc.ID, "llm", llmGrant)
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

const sampleSource = `# A tiny durable agent. Every llm/kv/log call is a memoized RPC: re-running
# this execution replays from the event log instead of re-spending the calls.
def main(input):
    name = input.get("name", "world")
    log("greeting", name)

    # Count how many times we've greeted this name (per-actor kv store).
    seen = kv_get("seen:" + name) or 0
    kv_set("seen:" + name, seen + 1)

    reply = llm([
        {"role": "system", "content": "You are a cheerful greeter."},
        {"role": "user", "content": "Greet " + name + " warmly in one sentence."},
    ])

    return {
        "greeting": reply["content"],
        "times_seen": seen + 1,
    }
`
