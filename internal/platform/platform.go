// Package platform wires the data-model store, capability executors, sandbox,
// and the durable engine into a single service. It resolves an execution's
// capability Environment from its pinned version's grants (the security boundary)
// and drives runs to completion.
package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/caps"
	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/store"
	"github.com/kylemaxwell/agentle/internal/vm"
)

// Config holds platform-wide defaults.
type Config struct {
	MaxSleep    time.Duration // cap on script sleep()
	ShellMaxCPU time.Duration // cap on a single shell command
}

// Service is the integration hub for the control plane and triggers.
type Service struct {
	Store   *store.Store
	Log     engine.Log
	Leaser  engine.Leaser
	Pool    engine.SandboxPool
	KV      caps.KVStore
	Runner  engine.Runner
	Cfg     Config
	LogSink caps.LogSink
}

// New builds a Service. log/leaser/pool/kv are injected so backends are swappable.
func New(st *store.Store, log engine.Log, leaser engine.Leaser, pool engine.SandboxPool, kv caps.KVStore, sink caps.LogSink, cfg Config) *Service {
	if cfg.MaxSleep == 0 {
		cfg.MaxSleep = 30 * time.Second
	}
	return &Service{
		Store: st, Log: log, Leaser: leaser, Pool: pool, KV: kv,
		Runner: &vm.Runner{}, Cfg: cfg, LogSink: sink,
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
	env, err := s.assembleEnv(ctx, exec, e.ScriptID, v.Grants)
	if err != nil {
		return engine.ExecutionSpec{}, err
	}
	return engine.ExecutionSpec{Source: v.Source, Image: v.Image, Input: e.Input, Env: env}, nil
}

// SetStatus implements engine.Resolver.
func (s *Service) SetStatus(ctx context.Context, exec engine.ExecutionID, status engine.Status, output json.RawMessage, errMsg string) error {
	return s.Store.SetExecutionStatus(ctx, string(exec), int(status), output, errMsg)
}

// assembleEnv builds the capability environment: system caps always, granted
// caps from resolved tool configs + secrets.
func (s *Service) assembleEnv(ctx context.Context, exec engine.ExecutionID, scriptID string, grants []store.GrantRef) (engine.Environment, error) {
	env := engine.Environment{
		"log":  caps.Log(exec, s.LogSink),
		"time": caps.Time(s.Cfg.MaxSleep),
		"rand": caps.Rand(exec),
		"kv":   caps.KV(s.KV, "script:"+scriptID), // per-actor namespace
	}
	for _, g := range grants {
		cfg, err := s.Store.GetToolConfig(ctx, g.ConfigID)
		if err != nil {
			return nil, fmt.Errorf("grant %q -> config %q: %w", g.Capability, g.ConfigID, err)
		}
		var secret string
		if cfg.SecretRef != "" {
			secret, err = s.Store.GetSecret(ctx, cfg.SecretRef)
			if err != nil {
				return nil, fmt.Errorf("config %q secret %q: %w", cfg.ID, cfg.SecretRef, err)
			}
		}
		exe, err := buildExecutor(g.Capability, cfg, secret)
		if err != nil {
			return nil, err
		}
		if exe != nil {
			env[g.Capability] = exe
		}
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

// RunExecution creates an execution row for (scriptID, version) and runs it to
// completion synchronously, returning the final execution record.
func (s *Service) RunExecution(ctx context.Context, scriptID string, version uint64, input json.RawMessage, trigger string) (*store.Execution, error) {
	sc, err := s.Store.GetScript(ctx, scriptID)
	if err != nil {
		return nil, err
	}
	if version == 0 {
		version = sc.CurrentVersion
	}
	if version == 0 {
		return nil, fmt.Errorf("script %q has no versions", scriptID)
	}
	v, err := s.Store.GetVersion(ctx, scriptID, version)
	if err != nil {
		return nil, err
	}

	id := "ex_" + uuid.NewString()
	exe := store.Execution{
		ID: id, ScriptID: scriptID, Version: version,
		Status: int(engine.StatusRunning), Input: input, Trigger: trigger,
	}
	if err := s.Store.CreateExecution(ctx, exe); err != nil {
		return nil, err
	}

	eng := &engine.Engine{Leaser: s.Leaser, Log: s.Log, Runner: s.Runner, Res: s}
	if usesShell(v.Grants) && s.Pool != nil {
		eng.Pool = s.Pool
	}
	if _, err := eng.Run(ctx, engine.ExecutionID(id)); err != nil {
		// Status already recorded as failed by the engine; surface the record.
		return s.Store.GetExecution(ctx, id)
	}
	return s.Store.GetExecution(ctx, id)
}
