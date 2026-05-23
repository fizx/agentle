package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/kylemaxwell/agentle/internal/examples"
	"github.com/kylemaxwell/agentle/internal/store"
)

// seed installs a default admin user, default tool configs (idempotent) and, on
// an empty database, sample scripts so the dashboard is immediately playable.
func seed(ctx context.Context, st *store.Store, log *slog.Logger) error {
	// Ensure an admin exists so identity/RBAC works out of the box.
	if n, err := st.CountUsers(ctx); err != nil {
		return err
	} else if n == 0 {
		if _, err := st.CreateUser(ctx, "u_admin", "admin", store.RoleAdmin); err != nil {
			return err
		}
		log.Info("seeded default admin user", "id", "u_admin")
	}

	// Always-present mock llm config (no credentials needed).
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "llm-mock", Capability: "llm", Config: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	// A conservative public http allowlist for demos.
	httpCfg, _ := json.Marshal(map[string]any{"allow": []string{"api.github.com", "*.githubusercontent.com", "httpbin.org"}})
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "http-public", Capability: "http", Config: httpCfg}); err != nil {
		return err
	}
	// A shell tool config so the shell example can be granted.
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "shell-local", Capability: "shell", Config: json.RawMessage(`{}`)}); err != nil {
		return err
	}
	// A demo MCP server config. Empty endpoint => in-process mock (echo/add/upper),
	// so the MCP examples are playable offline; set "endpoint" to a real MCP server
	// (e.g. this instance's own /mcp) to use the live JSON-RPC path.
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "mcp-demo", Capability: "mcp", Config: json.RawMessage(`{}`)}); err != nil {
		return err
	}

	llmGrant := "llm-mock"
	// If an API key is present in the environment, wire a real OpenAI-compatible config.
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		if err := st.PutSecret(ctx, "OPENAI_API_KEY", store.ScopeGlobal, key); err != nil {
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

	scripts, err := st.ListScripts(ctx, "", 1, 0)
	if err != nil {
		return err
	}
	if len(scripts) > 0 {
		return nil // don't clobber an existing workspace
	}

	hello, err := st.CreateScript(ctx, "sc_hello", "hello-agent", "u_admin")
	if err != nil {
		return err
	}
	if _, err := st.SaveVersion(ctx, hello.ID, examples.Find("hello").Source, "", []store.GrantRef{{Capability: "llm", ConfigID: llmGrant}}); err != nil {
		return err
	}

	sheller, err := st.CreateScript(ctx, "sc_shell", "shell-example", "u_admin")
	if err != nil {
		return err
	}
	if _, err := st.SaveVersion(ctx, sheller.ID, examples.Find("shell").Source, "", []store.GrantRef{{Capability: "shell", ConfigID: "shell-local"}}); err != nil {
		return err
	}
	log.Info("seeded sample scripts", "llm", llmGrant)
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
