package engine

import (
	"context"
	"sync"
)

// Leaser hands out exclusive ownership of an execution.
type Leaser interface {
	Acquire(ctx context.Context, exec ExecutionID) (Lease, error)
}

// Lease is the single-writer right over one execution. Its token must accompany
// every Append; ownership can be stolen on failover, invalidating the token.
type Lease interface {
	Token() FenceToken
	Renew(ctx context.Context) error // ErrLost if ownership was taken over
	Release(ctx context.Context) error
}

// MemLeaser is an in-memory Leaser. Each Acquire mints a strictly increasing
// fence token, immediately invalidating any prior holder — modeling failover
// takeover. It also implements FenceChecker for MemLog.
type MemLeaser struct {
	mu      sync.Mutex
	current map[ExecutionID]FenceToken
	next    FenceToken
}

func NewMemLeaser() *MemLeaser {
	return &MemLeaser{current: make(map[ExecutionID]FenceToken)}
}

func (m *MemLeaser) Acquire(_ context.Context, exec ExecutionID) (Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	m.current[exec] = m.next
	return &memLease{leaser: m, exec: exec, token: m.next}, nil
}

// Current implements FenceChecker.
func (m *MemLeaser) Current(exec ExecutionID) (FenceToken, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.current[exec]
	return t, ok
}

type memLease struct {
	leaser *MemLeaser
	exec   ExecutionID
	token  FenceToken
}

func (l *memLease) Token() FenceToken { return l.token }

func (l *memLease) Renew(_ context.Context) error {
	l.leaser.mu.Lock()
	defer l.leaser.mu.Unlock()
	if l.leaser.current[l.exec] != l.token {
		return ErrLost
	}
	return nil
}

func (l *memLease) Release(_ context.Context) error {
	l.leaser.mu.Lock()
	defer l.leaser.mu.Unlock()
	if l.leaser.current[l.exec] == l.token {
		delete(l.leaser.current, l.exec)
	}
	return nil
}
