package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

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

	scripts, err := st.ListScripts(ctx, 1, 0)
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
	if _, err := st.SaveVersion(ctx, hello.ID, sampleSource, "", []store.GrantRef{{Capability: "llm", ConfigID: llmGrant}}); err != nil {
		return err
	}

	sheller, err := st.CreateScript(ctx, "sc_shell", "shell-example", "u_admin")
	if err != nil {
		return err
	}
	if _, err := st.SaveVersion(ctx, sheller.ID, shellSource, "", []store.GrantRef{{Capability: "shell", ConfigID: "shell-local"}}); err != nil {
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

const sampleSource = `# A tiny durable agent. Every llm/store/log call is a memoized RPC: re-running
# this execution replays from the event log instead of re-spending the calls.
#
# main(input) receives a structured event: {id, kind, trigger_id, actor, data}.
# "data" is what the caller provided (run input, or a webhook body).
def main(input):
    data = input.get("data") or {}
    name = data.get("name", "world")
    log("greeting", name, "via", input["kind"])

    # Per-actor durable state. Manual runs are anonymous actors (no sharing);
    # a trigger can bind a named actor to share state across events.
    seen = fetch("seen:" + name) or 0
    store("seen:" + name, seen + 1)

    reply = llm([
        {"role": "system", "content": "You are a cheerful greeter."},
        {"role": "user", "content": "Greet " + name + " warmly in one sentence."},
    ])

    return {"greeting": reply["content"], "times_seen": seen + 1}
`

const shellSource = `# Shell capability: commands run in a per-actor sandbox home dir that is
# snapshotted to object storage on each fs mutation (see the trace barriers).
def main(input):
    data = input.get("data") or {}
    msg = data.get("message", "hello from the sandbox")

    shell(["sh", "-c", "echo '" + msg + "' > note.txt"])
    cat = shell(["cat", "note.txt"])
    uname = shell(["uname", "-a"])

    return {
        "exit": cat["code"],
        "note": cat["stdout"].strip(),
        "uname": uname["stdout"].strip(),
    }
`
