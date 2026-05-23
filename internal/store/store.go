// Package store is the durable data model: scripts and immutable versions, tool
// configs, secrets, grants, executions, triggers — plus the SQLite-backed event
// log and KV runtime state. Secrets are stored here and never returned to the VM
// or surfaced through read APIs.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

// GrantRef names a configured tool instance available to a script version. The
// referenced ToolConfig holds the endpoint/limits and a secret-ref.
type GrantRef struct {
	Capability string `json:"capability"` // name the script sees, e.g. "http"
	ConfigID   string `json:"config_id"`  // -> ToolConfig
}

// Script is a named program with a current (latest) version pointer.
type Script struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	CurrentVersion uint64 `json:"current_version"`
	CreatedAt      int64  `json:"created_at"`
}

// Version is an immutable snapshot of a script. Executions pin to one Version.
type Version struct {
	ScriptID  string     `json:"script_id"`
	Version   uint64     `json:"version"`
	Source    string     `json:"source"`
	Image     string     `json:"image"`
	Grants    []GrantRef `json:"grants"`
	CreatedAt int64      `json:"created_at"`
}

// ToolConfig is a pre-configured capability instance (endpoint + limits +
// optional secret-ref). Config is capability-specific JSON.
type ToolConfig struct {
	ID         string          `json:"id"`
	Capability string          `json:"capability"`
	Config     json.RawMessage `json:"config"`
	SecretRef  string          `json:"secret_ref,omitempty"`
	CreatedAt  int64           `json:"created_at"`
}

// Execution is one durable run of a pinned version.
type Execution struct {
	ID        string          `json:"id"`
	ScriptID  string          `json:"script_id"`
	Version   uint64          `json:"version"`
	Status    int             `json:"status"`
	Input     json.RawMessage `json:"input,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
	Trigger   string          `json:"trigger,omitempty"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

// Trigger fires executions: cron (spec = cron expression) or webhook (spec =
// opaque token in the inbound URL).
type Trigger struct {
	ID        string `json:"id"`
	ScriptID  string `json:"script_id"`
	Kind      string `json:"kind"` // "cron" | "webhook"
	Spec      string `json:"spec"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

// Store wraps a SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the SQLite database at dsn. Use ":memory:" only for
// single-connection cases; file paths are recommended for playtesting.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite is single-writer; serialize to avoid "database is locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(schema)
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS scripts (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  current_version INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS versions (
  script_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  source TEXT NOT NULL,
  image TEXT NOT NULL DEFAULT '',
  grants TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL,
  PRIMARY KEY (script_id, version)
);
CREATE TABLE IF NOT EXISTS tool_configs (
  id TEXT PRIMARY KEY,
  capability TEXT NOT NULL,
  config TEXT NOT NULL DEFAULT '{}',
  secret_ref TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS secrets (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS executions (
  id TEXT PRIMARY KEY,
  script_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  status INTEGER NOT NULL,
  input TEXT,
  output TEXT,
  error TEXT NOT NULL DEFAULT '',
  trigger TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_exec_script ON executions(script_id, created_at DESC);
CREATE TABLE IF NOT EXISTS triggers (
  id TEXT PRIMARY KEY,
  script_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  spec TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  exec TEXT NOT NULL,
  seq INTEGER NOT NULL,
  data TEXT NOT NULL,
  PRIMARY KEY (exec, seq)
);
CREATE TABLE IF NOT EXISTS kv (
  ns TEXT NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  PRIMARY KEY (ns, key)
);
`

func now() int64 { return time.Now().UnixNano() }

// --- Scripts & versions ---------------------------------------------------

func (s *Store) CreateScript(ctx context.Context, id, name string) (*Script, error) {
	sc := &Script{ID: id, Name: name, CreatedAt: now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO scripts(id,name,current_version,created_at) VALUES(?,?,0,?)`,
		sc.ID, sc.Name, sc.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sc, nil
}

func (s *Store) GetScript(ctx context.Context, id string) (*Script, error) {
	var sc Script
	err := s.db.QueryRowContext(ctx, `SELECT id,name,current_version,created_at FROM scripts WHERE id=?`, id).
		Scan(&sc.ID, &sc.Name, &sc.CurrentVersion, &sc.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &sc, err
}

func (s *Store) ListScripts(ctx context.Context) ([]Script, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,current_version,created_at FROM scripts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Script
	for rows.Next() {
		var sc Script
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.CurrentVersion, &sc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// SaveVersion appends a new immutable version and advances current_version.
func (s *Store) SaveVersion(ctx context.Context, scriptID, source, image string, grants []GrantRef) (*Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var cur uint64
	if err := tx.QueryRowContext(ctx, `SELECT current_version FROM scripts WHERE id=?`, scriptID).Scan(&cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	v := &Version{ScriptID: scriptID, Version: cur + 1, Source: source, Image: image, Grants: grants, CreatedAt: now()}
	if v.Grants == nil {
		v.Grants = []GrantRef{}
	}
	gj, _ := json.Marshal(v.Grants)
	if _, err := tx.ExecContext(ctx, `INSERT INTO versions(script_id,version,source,image,grants,created_at) VALUES(?,?,?,?,?,?)`,
		v.ScriptID, v.Version, v.Source, v.Image, string(gj), v.CreatedAt); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scripts SET current_version=? WHERE id=?`, v.Version, scriptID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return v, nil
}

func (s *Store) GetVersion(ctx context.Context, scriptID string, version uint64) (*Version, error) {
	var v Version
	var gj string
	err := s.db.QueryRowContext(ctx,
		`SELECT script_id,version,source,image,grants,created_at FROM versions WHERE script_id=? AND version=?`,
		scriptID, version).Scan(&v.ScriptID, &v.Version, &v.Source, &v.Image, &gj, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(gj), &v.Grants); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Store) ListVersions(ctx context.Context, scriptID string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT script_id,version,source,image,grants,created_at FROM versions WHERE script_id=? ORDER BY version DESC`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var gj string
		if err := rows.Scan(&v.ScriptID, &v.Version, &v.Source, &v.Image, &gj, &v.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(gj), &v.Grants)
		out = append(out, v)
	}
	return out, rows.Err()
}

// --- Tool configs & secrets ------------------------------------------------

func (s *Store) PutToolConfig(ctx context.Context, c ToolConfig) error {
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	if len(c.Config) == 0 {
		c.Config = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tool_configs(id,capability,config,secret_ref,created_at) VALUES(?,?,?,?,?)
         ON CONFLICT(id) DO UPDATE SET capability=excluded.capability, config=excluded.config, secret_ref=excluded.secret_ref`,
		c.ID, c.Capability, string(c.Config), c.SecretRef, c.CreatedAt)
	return err
}

func (s *Store) GetToolConfig(ctx context.Context, id string) (*ToolConfig, error) {
	var c ToolConfig
	var cfg string
	err := s.db.QueryRowContext(ctx, `SELECT id,capability,config,secret_ref,created_at FROM tool_configs WHERE id=?`, id).
		Scan(&c.ID, &c.Capability, &cfg, &c.SecretRef, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Config = json.RawMessage(cfg)
	return &c, nil
}

func (s *Store) ListToolConfigs(ctx context.Context) ([]ToolConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,capability,config,secret_ref,created_at FROM tool_configs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolConfig
	for rows.Next() {
		var c ToolConfig
		var cfg string
		if err := rows.Scan(&c.ID, &c.Capability, &cfg, &c.SecretRef, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Config = json.RawMessage(cfg)
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutSecret stores a secret value. Values are never returned by any read method.
func (s *Store) PutSecret(ctx context.Context, name, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO secrets(name,value,created_at) VALUES(?,?,?)
         ON CONFLICT(name) DO UPDATE SET value=excluded.value`, name, value, now())
	return err
}

// GetSecret resolves a secret value for capability binding. Internal use only —
// not exposed via the control-plane API.
func (s *Store) GetSecret(ctx context.Context, name string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM secrets WHERE name=?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// ListSecretNames returns only names — never values.
func (s *Store) ListSecretNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// --- Executions ------------------------------------------------------------

func (s *Store) CreateExecution(ctx context.Context, e Execution) error {
	if e.CreatedAt == 0 {
		e.CreatedAt = now()
	}
	e.UpdatedAt = e.CreatedAt
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO executions(id,script_id,version,status,input,output,error,trigger,created_at,updated_at)
         VALUES(?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.ScriptID, e.Version, e.Status, nullRaw(e.Input), nullRaw(e.Output), e.Error, e.Trigger, e.CreatedAt, e.UpdatedAt)
	return err
}

func (s *Store) SetExecutionStatus(ctx context.Context, id string, status int, output json.RawMessage, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE executions SET status=?, output=?, error=?, updated_at=? WHERE id=?`,
		status, nullRaw(output), errMsg, now(), id)
	return err
}

func (s *Store) GetExecution(ctx context.Context, id string) (*Execution, error) {
	var e Execution
	var input, output sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT id,script_id,version,status,input,output,error,trigger,created_at,updated_at FROM executions WHERE id=?`, id).
		Scan(&e.ID, &e.ScriptID, &e.Version, &e.Status, &input, &output, &e.Error, &e.Trigger, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if input.Valid {
		e.Input = json.RawMessage(input.String)
	}
	if output.Valid {
		e.Output = json.RawMessage(output.String)
	}
	return &e, nil
}

func (s *Store) ListExecutions(ctx context.Context, scriptID string, limit int) ([]Execution, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id,script_id,version,status,error,trigger,created_at,updated_at FROM executions`
	args := []any{}
	if scriptID != "" {
		q += ` WHERE script_id=?`
		args = append(args, scriptID)
	}
	q += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		if err := rows.Scan(&e.ID, &e.ScriptID, &e.Version, &e.Status, &e.Error, &e.Trigger, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Triggers --------------------------------------------------------------

func (s *Store) PutTrigger(ctx context.Context, t Trigger) error {
	if t.CreatedAt == 0 {
		t.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO triggers(id,script_id,kind,spec,enabled,created_at) VALUES(?,?,?,?,?,?)
         ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, spec=excluded.spec, enabled=excluded.enabled`,
		t.ID, t.ScriptID, t.Kind, t.Spec, boolInt(t.Enabled), t.CreatedAt)
	return err
}

func (s *Store) DeleteTrigger(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM triggers WHERE id=?`, id)
	return err
}

func (s *Store) ListTriggers(ctx context.Context, kind string) ([]Trigger, error) {
	q := `SELECT id,script_id,kind,spec,enabled,created_at FROM triggers`
	var args []any
	if kind != "" {
		q += ` WHERE kind=?`
		args = append(args, kind)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Trigger
	for rows.Next() {
		var t Trigger
		var en int
		if err := rows.Scan(&t.ID, &t.ScriptID, &t.Kind, &t.Spec, &en, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.Enabled = en != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// FindWebhookTrigger resolves an enabled webhook trigger by its token (spec).
func (s *Store) FindWebhookTrigger(ctx context.Context, token string) (*Trigger, error) {
	var t Trigger
	var en int
	err := s.db.QueryRowContext(ctx,
		`SELECT id,script_id,kind,spec,enabled,created_at FROM triggers WHERE kind='webhook' AND spec=?`, token).
		Scan(&t.ID, &t.ScriptID, &t.Kind, &t.Spec, &en, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = en != 0
	if !t.Enabled {
		return nil, ErrNotFound
	}
	return &t, nil
}

func nullRaw(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return string(r)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DB exposes the underlying handle for sub-stores (event log, kv).
func (s *Store) DB() *sql.DB { return s.db }
