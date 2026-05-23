package store

import (
	"context"
	"database/sql"
)

// Suspension is a parked execution awaiting a wake condition: a message at
// Workspace and/or the WakeAt deadline (unix nanos, 0 = none). The dispatcher
// resumes the execution when the condition is met. It is the durable counterpart
// of an engine.Suspension.
type Suspension struct {
	Exec      string `json:"exec"`
	Workspace string `json:"workspace"`
	WakeAt    int64  `json:"wake_at"`
	CreatedAt int64  `json:"created_at"`
}

// PutSuspension records (or replaces) the wake condition for a parked execution.
func (s *Store) PutSuspension(ctx context.Context, sp Suspension) error {
	if sp.CreatedAt == 0 {
		sp.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO suspensions(exec,workspace,wake_at,created_at) VALUES(?,?,?,?)
         ON CONFLICT(exec) DO UPDATE SET workspace=excluded.workspace, wake_at=excluded.wake_at`,
		sp.Exec, sp.Workspace, sp.WakeAt, sp.CreatedAt)
	return err
}

// DeleteSuspension clears an execution's parked state (it is resuming or done).
func (s *Store) DeleteSuspension(ctx context.Context, exec string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM suspensions WHERE exec=?`, exec)
	return err
}

// ListSuspensions returns all parked executions, oldest first.
func (s *Store) ListSuspensions(ctx context.Context) ([]Suspension, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT exec,workspace,wake_at,created_at FROM suspensions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suspension{}
	for rows.Next() {
		var sp Suspension
		if err := rows.Scan(&sp.Exec, &sp.Workspace, &sp.WakeAt, &sp.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// GetSuspension returns one execution's parked state, or ErrNotFound.
func (s *Store) GetSuspension(ctx context.Context, exec string) (*Suspension, error) {
	var sp Suspension
	err := s.db.QueryRowContext(ctx,
		`SELECT exec,workspace,wake_at,created_at FROM suspensions WHERE exec=?`, exec).
		Scan(&sp.Exec, &sp.Workspace, &sp.WakeAt, &sp.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	return &sp, err
}
