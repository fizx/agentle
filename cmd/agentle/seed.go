package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"

	"github.com/kylemaxwell/agentle/internal/caps"
	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/examples"
	"github.com/kylemaxwell/agentle/internal/platform"
	"github.com/kylemaxwell/agentle/internal/store"
)

// seed installs a default admin user, default tool configs (idempotent) and, on
// an empty database, sample scripts + eval fixtures so the dashboard (and the
// Evals tab) is immediately playable.
func seed(ctx context.Context, st *store.Store, svc *platform.Service, log *slog.Logger) error {
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
	// A demo capability plugin (Python) exposing a "reverse" MCP tool, plus an mcp
	// config that grants it. Requires python3 in the sandbox when actually called.
	if err := st.PutPlugin(ctx, store.Plugin{ID: "pl_demo", Name: "text-tools", Runtime: "python", Enabled: true, Source: demoPluginSource}); err != nil {
		return err
	}
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "mcp-plugin", Capability: "mcp", Config: json.RawMessage(`{"plugin_id":"pl_demo","name":"text-tools"}`)}); err != nil {
		return err
	}
	// Reconcile the native (Go) plugins from the registry into the store so they
	// appear in the plugins list (not editable). Each gets a grantable mcp config.
	if err := seedNativePlugins(ctx, st); err != nil {
		return err
	}

	llmGrant := "llm-mock"
	// If an API key is present in the environment, wire a real OpenAI-compatible config.
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		if err := st.PutSecret(ctx, "OPENAI_API_KEY", store.ScopeGlobal, key); err != nil {
			return err
		}
		base := envOr("OPENAI_BASE_URL", "https://api.openai.com/v1")
		model := envOr("OPENAI_MODEL", "gpt-5.5")
		cfg, _ := json.Marshal(map[string]any{"base_url": base, "model": model})
		if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "openai", Capability: "llm", Config: cfg, SecretRef: "OPENAI_API_KEY"}); err != nil {
			return err
		}
		llmGrant = "openai"
		log.Info("configured live llm from OPENAI_API_KEY", "model", model)
	}

	// A local Ollama llm config (OpenAI-compatible, needs no key) so the coding
	// assistant is a real model fully offline. Idempotent; harmless if Ollama
	// isn't running (calls just fail until it is).
	ollamaCfg, _ := json.Marshal(map[string]any{"base_url": "http://localhost:11434/v1", "model": "qwen2.5-coder:32b"})
	if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "ollama", Capability: "llm", Config: ollamaCfg}); err != nil {
		return err
	}

	// The coding-assistant harness: a real agentle script that powers the in-editor
	// agent panel (one execution per chat, bound to chat:{script}:{chat}). Prefer a
	// configured OpenAI key, else local Ollama, so the assistant is a real model out
	// of the box. Seeded once (independent of sample scripts) and left for the user.
	harnessLLM := llmGrant
	if harnessLLM == "llm-mock" {
		harnessLLM = "ollama"
	}
	if _, err := st.GetScript(ctx, "sc_coding_assistant"); errors.Is(err, store.ErrNotFound) {
		if _, err := st.CreateScript(ctx, "sc_coding_assistant", "coding-assistant", "u_admin"); err != nil {
			return err
		}
		if _, err := st.SaveVersion(ctx, "sc_coding_assistant", examples.Find("coding_agent").Source, "",
			[]store.GrantRef{{Capability: "llm", ConfigID: harnessLLM}}); err != nil {
			return err
		}
		log.Info("seeded coding-assistant harness", "llm", harnessLLM)
	}

	// Seed the sample scripts only on a fresh database, detected by the absence of
	// the canonical hello sample (a raw script count would now always be non-zero
	// because the assistant harness above is itself a script).
	if _, err := st.GetScript(ctx, "sc_hello"); err == nil {
		return nil // samples already present; don't clobber the workspace
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
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

	// Eval fixtures: real runs promoted to goldens (with authored criteria/persona)
	// so the Evals tab is playable on a fresh DB. Best-effort — never fatal to boot.
	seedEvalFixtures(ctx, st, svc, log)
	return nil
}

// seedEvalFixtures creates a small golden dataset by actually running scripts and
// promoting them — coverage replay works offline, and the authored criteria.md /
// persona.md make the judge, calibration, persona simulator and consistency gate
// ready the moment a real LLM is configured. It drives the interactive chat
// fixture turn-by-turn (RunExecution → PostMessage → Resume), which is fully
// synchronous and deterministic under the mock LLM.
//
// The fixture scripts are pinned to the mock llm config (not whatever live llm is
// configured) so seeding is fast, offline-safe and reproducible — coverage replay
// then works on any machine. The authored criteria/persona still light up the
// judge, calibration and simulator the moment a real llm grant is swapped in.
func seedEvalFixtures(ctx context.Context, st *store.Store, svc *platform.Service, log *slog.Logger) {
	const fixtureLLM = "llm-mock"
	mklog := func(step string, err error) bool {
		if err != nil {
			log.Warn("eval fixtures: skipped", "step", step, "err", err)
			return false
		}
		return true
	}

	// Fixture 1: a non-interactive greeter run → success golden with a rubric.
	greeter, err := st.CreateScript(ctx, "sc_eval_greeter", "eval: greeter", "u_admin")
	if !mklog("create greeter", err) {
		return
	}
	if _, err := st.SaveVersion(ctx, greeter.ID, examples.Find("hello").Source, "", []store.GrantRef{{Capability: "llm", ConfigID: fixtureLLM}}); !mklog("greeter version", err) {
		return
	}
	exe, err := svc.RunExecution(ctx, platform.RunRequest{ScriptID: greeter.ID, Kind: "dashboard", Data: json.RawMessage(`{"name":"Ada"}`)})
	if !mklog("greeter run", err) {
		return
	}
	if exe.Status == int(engine.StatusCompleted) {
		_ = st.SetFeedback(ctx, exe.ID, store.FeedbackUp, "seed fixture", "u_admin")
		g := store.Golden{
			ID: "gold_eval_greeter", ScriptID: greeter.ID, OriginExec: exe.ID, OriginVersion: exe.Version,
			Label:    store.GoldenSuccess,
			Criteria: "The agent greets the named person in a single, friendly sentence and reports how many times that name has been seen.",
			Note:     "seed fixture",
		}
		_ = mklog("greeter golden", st.CreateGolden(ctx, g))
	}

	// Fixture 2: an interactive chat, driven for two user turns then /quit → a
	// success golden carrying both a rubric and a persona (so the simulator +
	// consistency gate are exercisable, not just coverage).
	chat, err := st.CreateScript(ctx, "sc_eval_chat", "eval: chat assistant", "u_admin")
	if !mklog("create chat", err) {
		return
	}
	if _, err := st.SaveVersion(ctx, chat.ID, examples.Find("chat_ui").Source, "", []store.GrantRef{{Capability: "llm", ConfigID: fixtureLLM}}); !mklog("chat version", err) {
		return
	}
	turns := []string{"What's a must-see in Tokyo?", "How many days should I spend there?", "/quit"}
	chatExe, ok := driveChat(ctx, svc, chat.ID, turns, log)
	if ok && chatExe.Status == int(engine.StatusCompleted) {
		_ = st.SetFeedback(ctx, chatExe.ID, store.FeedbackUp, "seed fixture", "u_admin")
		g := store.Golden{
			ID: "gold_eval_chat", ScriptID: chat.ID, OriginExec: chatExe.ID, OriginVersion: chatExe.Version,
			Label:    store.GoldenSuccess,
			Criteria: "The assistant answers each question concisely and on-topic, and ends the session cleanly when the user sends /quit.",
			Persona: "---\non_unknown: improvise_consistent\nstyle: naive\ncontext: surface\n---\n" +
				"You are a curious first-time traveler planning a short trip to Tokyo. You ask a couple of brief, " +
				"practical questions about what to see and how long to stay, then end the conversation by sending /quit.",
			Note: "seed fixture",
		}
		_ = mklog("chat golden", st.CreateGolden(ctx, g))
	}
	log.Info("seeded eval fixtures", "scripts", 2)
}

// driveChat starts a chat script and feeds it the given user turns, resuming after
// each message, until it completes (the final turn is expected to be /quit).
func driveChat(ctx context.Context, svc *platform.Service, scriptID string, turns []string, log *slog.Logger) (*store.Execution, bool) {
	exe, err := svc.RunExecution(ctx, platform.RunRequest{ScriptID: scriptID, Kind: "dashboard"})
	if err != nil {
		log.Warn("eval fixtures: chat start failed", "err", err)
		return nil, false
	}
	for _, t := range turns {
		msg, _ := json.Marshal(map[string]string{"text": t})
		if err := svc.PostMessage(ctx, exe.ID, msg); err != nil {
			log.Warn("eval fixtures: chat post failed", "err", err)
			return nil, false
		}
		if err := svc.Resume(ctx, exe.ID); err != nil {
			log.Warn("eval fixtures: chat resume failed", "err", err)
			return nil, false
		}
		cur, err := svc.Store.GetExecution(ctx, exe.ID)
		if err != nil {
			return nil, false
		}
		exe = cur
		if exe.Status == int(engine.StatusCompleted) || exe.Status == int(engine.StatusFailed) {
			break
		}
	}
	return exe, true
}

// seedNativePlugins mirrors the Go native-plugin registry into the store (so the
// dashboard lists them) and grants each a matching mcp tool config. Idempotent:
// PutPlugin upserts and only versions on a content change, so re-seeding a native
// plugin whose code version is unchanged is a no-op for its version history.
func seedNativePlugins(ctx context.Context, st *store.Store) error {
	for _, np := range caps.NativePlugins() {
		if err := st.PutPlugin(ctx, store.Plugin{
			ID:      np.ID,
			Name:    np.Name,
			Kind:    store.PluginNative,
			Runtime: store.PluginNative,
			Enabled: true,
		}); err != nil {
			return err
		}
		cfg, _ := json.Marshal(map[string]any{"plugin_id": np.ID, "name": np.Name})
		if err := st.PutToolConfig(ctx, store.ToolConfig{ID: "mcp-" + np.ID, Capability: "mcp", Config: cfg}); err != nil {
			return err
		}
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// demoPluginSource is a Python capability plugin providing a "reverse" MCP tool.
// argv: [-c, <subcmd>, <tool>, <args-json>] (so sys.argv[1]=subcmd, [3]=args).
const demoPluginSource = `import sys, json
cmd = sys.argv[1] if len(sys.argv) > 1 else ""
if cmd == "list":
    print(json.dumps([
        {"name": "reverse", "description": "reverse text",
         "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}},
    ]))
elif cmd == "call":
    args = json.loads(sys.argv[3]) if len(sys.argv) > 3 else {}
    print(args.get("text", "")[::-1])
`
