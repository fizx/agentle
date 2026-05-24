// Package platform wires the data-model store, capability executors, sandbox,
// and the durable engine into a single service. It resolves an execution's
// capability Environment from its pinned version's grants (the security boundary)
// and drives runs to completion.
package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/caps"
	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/pricing"
	"github.com/kylemaxwell/agentle/internal/secrets"
	"github.com/kylemaxwell/agentle/internal/store"
	"github.com/kylemaxwell/agentle/internal/vm"
)

// Config holds platform-wide defaults.
type Config struct {
	MaxSleep    time.Duration // cap on script sleep()
	ShellMaxCPU time.Duration // cap on a single shell command
	Debounce    time.Duration // min interval between fs snapshot barriers (0 = engine default)
}

// Service is the integration hub for the control plane and triggers.
type Service struct {
	Store   *store.Store
	Log     engine.Log
	Leaser  engine.Leaser
	Pool    engine.SandboxPool
	KV      caps.KVStore
	Inbox   caps.MessageQueue
	Runner  engine.Runner
	Cfg     Config
	LogSink caps.LogSink
	Pricing *pricing.Service // optional; nil => cost recorded as $0
	Secrets secrets.Store    // pluggable secret backend (defaults to SQLite)

	resumeMu sync.Mutex          // guards resuming
	resuming map[string]struct{} // executions with a resume in flight (dedup)
}

// New builds a Service. log/leaser/pool/kv/inbox are injected so backends are swappable.
func New(st *store.Store, log engine.Log, leaser engine.Leaser, pool engine.SandboxPool, kv caps.KVStore, inbox caps.MessageQueue, sink caps.LogSink, cfg Config) *Service {
	if cfg.MaxSleep == 0 {
		cfg.MaxSleep = 30 * time.Second
	}
	return &Service{
		Store: st, Log: log, Leaser: leaser, Pool: pool, KV: kv, Inbox: inbox,
		Runner: &vm.Runner{}, Cfg: cfg, LogSink: sink,
		Secrets:  secrets.SQLite(st),
		resuming: make(map[string]struct{}),
	}
}

// Resolve implements engine.Resolver: it loads the pinned version and assembles
// the capability environment from its grants. Only granted capabilities (plus the
// always-available system caps) appear — this is where the capability security
// model is enforced.
func (s *Service) Resolve(ctx context.Context, exec engine.ExecutionID) (engine.ExecutionSpec, error) {
	e, err := s.Store.GetExecution(ctx, string(exec))
	if err != nil {
		return engine.ExecutionSpec{}, err
	}
	v, err := s.Store.GetVersion(ctx, e.ScriptID, e.Version)
	if err != nil {
		return engine.ExecutionSpec{}, err
	}
	env, err := s.assembleEnv(ctx, exec, e.ScriptID, e.ActorID, v.Grants)
	if err != nil {
		return engine.ExecutionSpec{}, err
	}
	return engine.ExecutionSpec{Source: v.Source, Image: v.Image, Input: e.Input, Env: env}, nil
}

// SetStatus implements engine.Resolver. On completion it records token usage +
// cost for the run's LLM calls (best-effort; never fails the run).
func (s *Service) SetStatus(ctx context.Context, exec engine.ExecutionID, status engine.Status, output json.RawMessage, errMsg string) error {
	if err := s.Store.SetExecutionStatus(ctx, string(exec), int(status), output, errMsg); err != nil {
		return err
	}
	if status == engine.StatusCompleted {
		s.recordUsage(ctx, string(exec))
	}
	return nil
}

// Suspend implements engine.Resolver: it marks the execution suspended and records
// the wake condition (a workspace inbox and/or a deadline). The dispatcher resumes
// it when the condition is met.
func (s *Service) Suspend(ctx context.Context, exec engine.ExecutionID, susp engine.Suspension) error {
	if err := s.Store.SetExecutionStatus(ctx, string(exec), int(engine.StatusSuspended), nil, ""); err != nil {
		return err
	}
	return s.Store.PutSuspension(ctx, store.Suspension{
		Exec:      string(exec),
		Workspace: susp.Workspace,
		WakeAt:    susp.WakeAt,
	})
}

// assembleEnv builds the capability environment: system caps always, granted
// caps from resolved tool configs + secrets.
func (s *Service) assembleEnv(ctx context.Context, exec engine.ExecutionID, scriptID, actorID string, grants []store.GrantRef) (engine.Environment, error) {
	if actorID == "" {
		actorID = "exec:" + string(exec)
	}
	env := engine.Environment{
		"log":  caps.Log(exec, s.LogSink),
		"time": caps.Time(s.Cfg.MaxSleep),
		"rand": caps.Rand(exec),
		"kv":   caps.KV(s.KV, actorID), // namespaced by workspace, not script
	}
	if s.Inbox != nil {
		env["inbox"] = caps.Inbox(s.Inbox, actorID)
	}
	var mcpServers []caps.MCPServer // accumulated across all mcp grants (multi-server)
	for _, g := range grants {
		cfg, err := s.Store.GetToolConfig(ctx, g.ConfigID)
		if err != nil {
			return nil, fmt.Errorf("grant %q -> config %q: %w", g.Capability, g.ConfigID, err)
		}
		var secret string
		if cfg.SecretRef != "" {
			secret, err = s.Secrets.Resolve(ctx, cfg.SecretRef, scriptID)
			if err != nil {
				return nil, fmt.Errorf("config %q secret %q: %w", cfg.ID, cfg.SecretRef, err)
			}
		}
		if g.Capability == "mcp" {
			var c struct {
				Endpoint string `json:"endpoint"`
				Name     string `json:"name"`
				PluginID string `json:"plugin_id"`
			}
			_ = json.Unmarshal(cfg.Config, &c)
			name := c.Name
			if name == "" {
				name = cfg.ID
			}
			srv := caps.MCPServer{Name: name, Endpoint: c.Endpoint, APIKey: secret}
			if c.PluginID != "" {
				p, err := s.Store.GetPlugin(ctx, c.PluginID)
				if err != nil {
					return nil, fmt.Errorf("mcp config %q -> plugin %q: %w", cfg.ID, c.PluginID, err)
				}
				if !p.Enabled {
					return nil, fmt.Errorf("mcp config %q references disabled plugin %q", cfg.ID, c.PluginID)
				}
				srv.Plugin = &caps.PluginSpec{Pool: s.Pool, Runtime: p.Runtime, Source: p.Source}
			}
			mcpServers = append(mcpServers, srv)
			continue
		}
		exe, err := buildExecutor(g.Capability, cfg, secret)
		if err != nil {
			return nil, err
		}
		if exe != nil {
			env[g.Capability] = exe
		}
	}
	// Register the MCP router only when at least one server is granted, so an
	// ungranted mcp_list_tools() returns "not granted" (which the builtin turns
	// into an empty list) rather than emitting an empty RPC for every script.
	if len(mcpServers) > 0 {
		env["mcp"] = caps.MCPRouter(mcpServers)
	}
	return env, nil
}

// usesShell reports whether any grant is the shell capability (=> provision a sandbox).
func usesShell(grants []store.GrantRef) bool {
	for _, g := range grants {
		if g.Capability == "shell" {
			return true
		}
	}
	return false
}

func buildExecutor(capability string, cfg *store.ToolConfig, secret string) (engine.Executor, error) {
	switch capability {
	case "http":
		var c struct {
			Allow        []string `json:"allow"`
			AllowPrivate bool     `json:"allow_private"`
			AuthHeader   string   `json:"auth_header"`
			TimeoutSec   int      `json:"timeout_sec"`
		}
		_ = json.Unmarshal(cfg.Config, &c)
		hc := caps.HTTPConfig{Allow: c.Allow, AllowPrivate: c.AllowPrivate}
		if c.TimeoutSec > 0 {
			hc.Timeout = time.Duration(c.TimeoutSec) * time.Second
		}
		if c.AuthHeader != "" && secret != "" {
			hc.Headers = map[string]string{c.AuthHeader: secret}
		}
		return caps.HTTP(hc), nil
	case "llm":
		var c struct {
			BaseURL string `json:"base_url"`
			Model   string `json:"model"`
		}
		_ = json.Unmarshal(cfg.Config, &c)
		return caps.LLM(caps.LLMConfig{BaseURL: c.BaseURL, APIKey: secret, Model: c.Model}), nil
	case "shell":
		// Provisioned as a sandbox by the engine; no Environment executor.
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown capability %q in grant", capability)
	}
}

// RunRequest describes how to start an execution. The platform builds the
// structured event envelope and resolves the actor before running.
type RunRequest struct {
	ScriptID      string          // required
	Version       uint64          // 0 = current
	Kind          string          // "dashboard" | "webhook" | "cron"
	TriggerID     string          // empty for dashboard runs
	ActorTemplate string          // optional mustache, e.g. "webhook-{{event.id}}"
	EventID       string          // optional inbound event id (else generated)
	Data          json.RawMessage // user input / parsed webhook body
}

// RunExecution builds the event envelope, resolves the actor (kv namespace),
// creates the execution row, and runs it to completion synchronously.
func (s *Service) RunExecution(ctx context.Context, req RunRequest) (*store.Execution, error) {
	sc, err := s.Store.GetScript(ctx, req.ScriptID)
	if err != nil {
		return nil, err
	}
	version := req.Version
	if version == 0 {
		version = sc.CurrentVersion
	}
	if version == 0 {
		return nil, fmt.Errorf("script %q has no versions", req.ScriptID)
	}
	v, err := s.Store.GetVersion(ctx, req.ScriptID, version)
	if err != nil {
		return nil, err
	}

	id := "ex_" + uuid.NewString()
	envelope, actorID := buildEnvelope(id, req)
	input, _ := json.Marshal(envelope)

	exe := store.Execution{
		ID: id, ScriptID: req.ScriptID, Version: version, ActorID: actorID,
		Status: int(engine.StatusRunning), Input: input, Trigger: triggerLabel(req),
	}
	if err := s.Store.CreateExecution(ctx, exe); err != nil {
		return nil, err
	}

	if _, err := s.newEngine(v.Grants).Run(ctx, engine.ExecutionID(id)); err != nil {
		return s.Store.GetExecution(ctx, id) // status already recorded as failed
	}
	return s.Store.GetExecution(ctx, id)
}

// newEngine builds an engine for a run with the given grants, attaching the
// sandbox pool only when the shell capability is used.
func (s *Service) newEngine(grants []store.GrantRef) *engine.Engine {
	eng := &engine.Engine{Leaser: s.Leaser, Log: s.Log, Runner: s.Runner, Res: s, Debounce: s.Cfg.Debounce}
	if usesShell(grants) && s.Pool != nil {
		eng.Pool = s.Pool
	}
	return eng
}
