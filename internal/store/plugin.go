package store

import (
	"context"
	"database/sql"
	"errors"
)

// Plugin kinds. A "script" plugin is user-authored source run per-call in the
// sandbox; a "native" plugin is implemented in Go and registered in code (it
// appears in the list but its source is not editable).
const (
	PluginScript = "script"
	PluginNative = "native"
)

// Plugin is an agentle-managed capability extension: a small program that the
// platform runs to provide MCP tools. Script plugins speak a simple convention:
// argv[1]="list" prints the tool catalog as JSON; "call" with argv[2]=tool and
// argv[3]=args-JSON prints the tool result. Native plugins are Go code keyed by
// id in the native registry; their Runtime is "native" and Source is empty.
//
// Source/Runtime are versioned: each content change appends a PluginVersion and
// advances CurrentVersion, so a plugin can be rolled back like a script.
type Plugin struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`    // "script" (default) | "native"
	Runtime        string `json:"runtime"` // python | node | bash | native
	Source         string `json:"source"`
	Enabled        bool   `json:"enabled"`
	CurrentVersion uint64 `json:"current_version"`
	CreatedAt      int64  `json:"created_at"`
}

// PluginVersion is an immutable snapshot of a plugin's runtime+source.
type PluginVersion struct {
	PluginID  string `json:"plugin_id"`
	Version   uint64 `json:"version"`
	Runtime   string `json:"runtime"`
	Source    string `json:"source"`
	Note      string `json:"note,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// PutPlugin upserts a plugin by id. When the runtime or source differs from the
// current version (or the plugin has no versions yet), it appends a new
// PluginVersion and advances current_version — so saves are tracked like a
// script's. Name/enabled/kind are mutable metadata and are not versioned.
func (s *Store) PutPlugin(ctx context.Context, p Plugin) error {
	if p.CreatedAt == 0 {
		p.CreatedAt = now()
	}
	if p.Runtime == "" {
		p.Runtime = "python"
	}
	if p.Kind == "" {
		p.Kind = PluginScript
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Current content, if the plugin already exists.
	var curVer uint64
	var curRuntime, curSource string
	exists := true
	err = tx.QueryRowContext(ctx, `SELECT current_version,runtime,source FROM plugins WHERE id=?`, p.ID).
		Scan(&curVer, &curRuntime, &curSource)
	if errors.Is(err, sql.ErrNoRows) {
		exists = false
	} else if err != nil {
		return err
	}

	changed := !exists || curRuntime != p.Runtime || curSource != p.Source
	newVer := curVer
	if changed {
		newVer = curVer + 1
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO plugins(id,name,kind,runtime,source,enabled,current_version,created_at) VALUES(?,?,?,?,?,?,?,?)
         ON CONFLICT(id) DO UPDATE SET name=excluded.name, kind=excluded.kind, runtime=excluded.runtime,
           source=excluded.source, enabled=excluded.enabled, current_version=excluded.current_version`,
		p.ID, p.Name, p.Kind, p.Runtime, p.Source, boolInt(p.Enabled), newVer, p.CreatedAt); err != nil {
		return err
	}
	if changed {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO plugin_versions(plugin_id,version,runtime,source,note,created_at) VALUES(?,?,?,?,?,?)`,
			p.ID, newVer, p.Runtime, p.Source, "", now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetPlugin(ctx context.Context, id string) (*Plugin, error) {
	var p Plugin
	var en int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,kind,runtime,source,enabled,current_version,created_at FROM plugins WHERE id=?`, id).
		Scan(&p.ID, &p.Name, &p.Kind, &p.Runtime, &p.Source, &en, &p.CurrentVersion, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	p.Enabled = en != 0
	return &p, err
}

func (s *Store) ListPlugins(ctx context.Context) ([]Plugin, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,kind,runtime,source,enabled,current_version,created_at FROM plugins ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plugin{}
	for rows.Next() {
		var p Plugin
		var en int
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Runtime, &p.Source, &en, &p.CurrentVersion, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Enabled = en != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePlugin(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_versions WHERE plugin_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugins WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ListPluginVersions returns a plugin's version history, newest first.
func (s *Store) ListPluginVersions(ctx context.Context, pluginID string) ([]PluginVersion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT plugin_id,version,runtime,source,note,created_at FROM plugin_versions WHERE plugin_id=? ORDER BY version DESC`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginVersion{}
	for rows.Next() {
		var v PluginVersion
		if err := rows.Scan(&v.PluginID, &v.Version, &v.Runtime, &v.Source, &v.Note, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetPluginVersion returns a single historical version of a plugin.
func (s *Store) GetPluginVersion(ctx context.Context, pluginID string, version uint64) (*PluginVersion, error) {
	var v PluginVersion
	err := s.db.QueryRowContext(ctx,
		`SELECT plugin_id,version,runtime,source,note,created_at FROM plugin_versions WHERE plugin_id=? AND version=?`,
		pluginID, version).Scan(&v.PluginID, &v.Version, &v.Runtime, &v.Source, &v.Note, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &v, err
}
