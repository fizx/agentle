package engine

import (
	"context"
	"sync"
)

// Log is the durable, ordered, single-writer-per-execution event store.
// Backends: in-memory (tests), SQLite (durable tier). Redis+AOF in prod.
type Log interface {
	// Append atomically writes ev at expectedSeq for exec, fenced by token.
	//   - ErrConflict if expectedSeq != the actual next seq (stale/duplicate writer)
	//   - ErrFenced   if token is no longer the current lease token
	// If durable is true, returns only after the event is durably persisted;
	// otherwise may return after a best-effort write. Callers pass durable=true
	// for RPC intents and fs barriers, false for idempotent results.
	//
	// The assigned Seq is written into ev.Seq by the implementation and returned.
	Append(ctx context.Context, exec ExecutionID, expectedSeq Seq, token FenceToken, ev Event, durable bool) (Seq, error)

	// Read returns events for exec from fromSeq (inclusive), in order.
	Read(ctx context.Context, exec ExecutionID, fromSeq Seq) ([]Event, error)
}

// FenceChecker lets a Log reject appends from a superseded lease. The in-memory
// Leaser implements it; a distributed backend would consult its own store.
type FenceChecker interface {
	Current(exec ExecutionID) (FenceToken, bool)
}

// MemLog is an in-memory Log. It enforces single-writer via CAS on the next seq
// and (optionally) fencing via a FenceChecker. Safe for concurrent Append.
type MemLog struct {
	mu     sync.Mutex
	events map[ExecutionID][]Event
	fences FenceChecker // optional; nil disables fence enforcement
}

// NewMemLog returns an empty in-memory log. fences may be nil.
func NewMemLog(fences FenceChecker) *MemLog {
	return &MemLog{events: make(map[ExecutionID][]Event), fences: fences}
}

func (l *MemLog) Append(_ context.Context, exec ExecutionID, expectedSeq Seq, token FenceToken, ev Event, _ bool) (Seq, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.fences != nil {
		if cur, ok := l.fences.Current(exec); ok && token != cur {
			return 0, ErrFenced
		}
	}

	next := Seq(len(l.events[exec]))
	if expectedSeq != next {
		return 0, ErrConflict
	}
	ev.Seq = next
	l.events[exec] = append(l.events[exec], ev)
	return next, nil
}

func (l *MemLog) Read(_ context.Context, exec ExecutionID, fromSeq Seq) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	all := l.events[exec]
	if int(fromSeq) >= len(all) {
		return nil, nil
	}
	out := make([]Event, len(all)-int(fromSeq))
	copy(out, all[fromSeq:])
	return out, nil
}
