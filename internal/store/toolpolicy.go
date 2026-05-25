package store

import (
	"context"
	"database/sql"
	"errors"
)

// Policy sources, in increasing trust. A wrong "read" tag risks a real side
// effect; a wrong "write" tag only over-gates — so default is write.
const (
	PolicyOperator   = "operator"   // a human set this explicitly (highest trust)
	PolicyAnnotation = "annotation" // seeded from an MCP tool's hints (advisory)
)

// ToolPolicy is the local read/write classification for one external tool, keyed
// by server/tool. It is consulted only on a cassette MISS, to decide whether a
// novel call may run unattended (read) or must gate (write). It is not a
// correctness mechanism — hits are safe regardless. For HTTP the convention is
// server = request host, tool = uppercase method.
type ToolPolicy struct {
	Server    string `json:"server"`
	Tool      string `json:"tool"`
	IsWrite   bool   `json:"is_write"`
	Source    string `json:"source"`
	CreatedAt int64  `json:"created_at"`
}

// PutToolPolicy upserts a classification (operator override or annotation seed).
func (s *Store) PutToolPolicy(ctx context.Context, tp ToolPolicy) error {
	if tp.CreatedAt == 0 {
		tp.CreatedAt = now()
	}
	if tp.Source == "" {
		tp.Source = PolicyOperator
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tool_policy(server,tool,is_write,source,created_at) VALUES(?,?,?,?,?)
         ON CONFLICT(server,tool) DO UPDATE SET is_write=excluded.is_write, source=excluded.source`,
		tp.Server, tp.Tool, boolInt(tp.IsWrite), tp.Source, tp.CreatedAt)
	return err
}

// GetToolPolicy returns the policy for (server, tool) and whether one is set.
func (s *Store) GetToolPolicy(ctx context.Context, server, tool string) (*ToolPolicy, bool, error) {
	var tp ToolPolicy
	var w int
	err := s.db.QueryRowContext(ctx,
		`SELECT server,tool,is_write,source,created_at FROM tool_policy WHERE server=? AND tool=?`, server, tool).
		Scan(&tp.Server, &tp.Tool, &w, &tp.Source, &tp.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	tp.IsWrite = w != 0
	return &tp, true, nil
}

// ListToolPolicies returns all classifications, ordered by server then tool.
func (s *Store) ListToolPolicies(ctx context.Context) ([]ToolPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server,tool,is_write,source,created_at FROM tool_policy ORDER BY server, tool`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolPolicy{}
	for rows.Next() {
		var tp ToolPolicy
		var w int
		if err := rows.Scan(&tp.Server, &tp.Tool, &w, &tp.Source, &tp.CreatedAt); err != nil {
			return nil, err
		}
		tp.IsWrite = w != 0
		out = append(out, tp)
	}
	return out, rows.Err()
}

func (s *Store) DeleteToolPolicy(ctx context.Context, server, tool string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tool_policy WHERE server=? AND tool=?`, server, tool)
	return err
}
