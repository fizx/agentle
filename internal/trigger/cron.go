// Package trigger turns cron schedules and inbound webhooks into executions.
// Triggers are anonymous actors that dispatch to a named script.
package trigger

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/kylemaxwell/agentle/internal/store"
	"github.com/robfig/cron/v3"
)

// Runner is the subset of the platform service the scheduler needs.
type Runner interface {
	RunExecution(ctx context.Context, scriptID string, version uint64, input json.RawMessage, trigger string) (*store.Execution, error)
}

// Scheduler runs cron triggers. Call Reload after the trigger set changes.
type Scheduler struct {
	store  *store.Store
	runner Runner
	log    *slog.Logger

	mu   sync.Mutex
	cron *cron.Cron
}

func NewScheduler(st *store.Store, runner Runner, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{store: st, runner: runner, log: log}
}

// Reload rebuilds the cron schedule from the store's enabled cron triggers.
func (s *Scheduler) Reload(ctx context.Context) error {
	triggers, err := s.store.ListTriggers(ctx, "cron")
	if err != nil {
		return err
	}
	c := cron.New()
	for _, t := range triggers {
		if !t.Enabled {
			continue
		}
		t := t
		if _, err := c.AddFunc(t.Spec, func() { s.fire(t) }); err != nil {
			s.log.Warn("skipping invalid cron trigger", "id", t.ID, "spec", t.Spec, "err", err)
		}
	}
	s.mu.Lock()
	old := s.cron
	s.cron = c
	s.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	c.Start()
	return nil
}

func (s *Scheduler) fire(t store.Trigger) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	input, _ := json.Marshal(map[string]any{"trigger": "cron", "trigger_id": t.ID, "fired_at": time.Now().UnixNano()})
	exe, err := s.runner.RunExecution(ctx, t.ScriptID, 0, input, "cron:"+t.ID)
	if err != nil {
		s.log.Error("cron trigger run failed", "trigger", t.ID, "script", t.ScriptID, "err", err)
		return
	}
	s.log.Info("cron trigger fired", "trigger", t.ID, "script", t.ScriptID, "execution", exe.ID, "status", exe.Status)
}

// Stop halts scheduled jobs.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
}
