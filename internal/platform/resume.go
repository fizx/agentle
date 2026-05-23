package platform

import (
	"context"
	"log/slog"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/store"
)

// maxPumpRounds bounds one Pump pass. A resumed run can send messages that wake
// other actors, so Pump loops; this cap prevents a runaway cascade from spinning
// forever within a single pass (the next tick picks up any remainder).
const maxPumpRounds = 1000

// Resume re-runs a suspended execution: it replays the durable event log and
// continues from the suspension point (recv now finds its message, or its
// deadline has passed). Safe to call concurrently — an in-flight guard makes a
// duplicate resume for the same execution a no-op.
func (s *Service) Resume(ctx context.Context, execID string) error {
	if !s.claimResume(execID) {
		return nil
	}
	defer s.releaseResume(execID)

	e, err := s.Store.GetExecution(ctx, execID)
	if err != nil {
		return err
	}
	if e.Status != int(engine.StatusSuspended) {
		// Already progressed (raced with another resumer or the engine). Drop any
		// stale suspension row and stop.
		return s.Store.DeleteSuspension(ctx, execID)
	}
	v, err := s.Store.GetVersion(ctx, e.ScriptID, e.Version)
	if err != nil {
		return err
	}
	// Clear the parked state and flip to running before the engine takes over. If
	// the run suspends again, the engine records a fresh suspension.
	if err := s.Store.DeleteSuspension(ctx, execID); err != nil {
		return err
	}
	if err := s.Store.SetExecutionStatus(ctx, execID, int(engine.StatusRunning), nil, ""); err != nil {
		return err
	}
	_, err = s.newEngine(v.Grants).Run(ctx, engine.ExecutionID(execID))
	return err
}

// Pump resumes every suspended execution whose wake condition is now met: its
// workspace inbox has an unconsumed message, or its deadline has passed. It loops
// until a pass makes no progress, because a resumed run may itself send messages
// that wake other actors.
func (s *Service) Pump(ctx context.Context, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	for round := 0; round < maxPumpRounds; round++ {
		susps, err := s.Store.ListSuspensions(ctx)
		if err != nil {
			log.Warn("dispatcher: list suspensions failed", "err", err)
			return
		}
		now := time.Now().UnixNano()
		progressed := false
		for _, sp := range susps {
			if !s.ready(ctx, sp, now) {
				continue
			}
			if err := s.Resume(ctx, sp.Exec); err != nil {
				log.Warn("dispatcher: resume failed", "exec", sp.Exec, "err", err)
			}
			progressed = true
		}
		if !progressed {
			return
		}
	}
	log.Warn("dispatcher: pump hit round cap; deferring remainder to next tick")
}

// ready reports whether a parked execution's wake condition is satisfied: its
// deadline has passed, or its workspace inbox has an unconsumed message.
func (s *Service) ready(ctx context.Context, sp store.Suspension, now int64) bool {
	if sp.WakeAt > 0 && now >= sp.WakeAt {
		return true
	}
	if sp.Workspace != "" {
		if n, err := s.Store.Inbox().InboxDepth(ctx, sp.Workspace); err == nil && n > 0 {
			return true
		}
	}
	return false
}

// RunDispatcher drives Pump on an interval until ctx is cancelled, plus once at
// startup to recover executions whose wake condition was met while the process
// was down. Run it in its own goroutine.
func (s *Service) RunDispatcher(ctx context.Context, interval time.Duration, log *slog.Logger) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	s.Pump(ctx, log) // startup recovery
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Pump(ctx, log)
		}
	}
}

func (s *Service) claimResume(execID string) bool {
	s.resumeMu.Lock()
	defer s.resumeMu.Unlock()
	if _, busy := s.resuming[execID]; busy {
		return false
	}
	s.resuming[execID] = struct{}{}
	return true
}

func (s *Service) releaseResume(execID string) {
	s.resumeMu.Lock()
	delete(s.resuming, execID)
	s.resumeMu.Unlock()
}
