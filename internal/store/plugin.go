package store

import (
	"context"
	"database/sql"
	"errors"
)

// Plugin is an agentle-managed capability extension: a small program (in some
// runtime) that the platform runs in the sandbox to provide MCP tools. It speaks
// a simple convention: argv[1]="list" prints the tool catalog as JSON; "call"
// with argv[2]=tool and argv[3]=args-JSON prints the tool result.
type Plugin struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Runtime   string `json:"runtime"` // python | node | bash
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// PutPlugin upserts a plugin by id.
func (s *Store) PutPlugin(ctx context.Context, p Plugin) error {
	if p.CreatedAt == 0 {
		p.CreatedAt = now()
	}
	if p.Runtime == "" {
		p.Runtime = "python"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO plugins(id,name,runtime,source,enabled,created_at) VALUES(?,?,?,?,?,?)
         ON CONFLICT(id) DO UPDATE SET name=excluded.name, runtime=excluded.runtime, source=excluded.source, enabled=excluded.enabled`,
		p.ID, p.Name, p.Runtime, p.Source, boolInt(p.Enabled), p.CreatedAt)
	return err
}

func (s *Store) GetPlugin(ctx context.Context, id string) (*Plugin, error) {
	var p Plugin
	var en int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,runtime,source,enabled,created_at FROM plugins WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Runtime, &p.Source, &en, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	p.Enabled = en != 0
	return &p, err
}

func (s *Store) ListPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,runtime,source,enabled,created_at FROM plugins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plugin{}
	for rows.Next() {
		var p Plugin
		var en int
		if err := rows.Scan(&p.ID, &p.Name, &p.Runtime, &p.Source, &en, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Enabled = en != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePlugin(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM plugins WHERE id=?`, id)
	return err
}
