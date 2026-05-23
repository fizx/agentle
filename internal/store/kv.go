package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// KV is a durable caps.KVStore backed by SQLite (per-actor namespace).
type KV struct{ db *sql.DB }

// KV returns the per-actor key/value store over this database.
func (s *Store) KV() *KV { return &KV{db: s.db} }

func (k *KV) Get(ctx context.Context, ns, key string) (json.RawMessage, bool, error) {
	var v string
	err := k.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE ns=? AND key=?`, ns, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return json.RawMessage(v), true, nil
}

func (k *KV) Set(ctx context.Context, ns, key string, val json.RawMessage) error {
	if len(val) == 0 {
		val = json.RawMessage("null")
	}
	_, err := k.db.ExecContext(ctx,
		`INSERT INTO kv(ns,key,value) VALUES(?,?,?) ON CONFLICT(ns,key) DO UPDATE SET value=excluded.value`,
		ns, key, string(val))
	return err
}

func (k *KV) List(ctx context.Context, ns, prefix string) ([]string, error) {
	rows, err := k.db.QueryContext(ctx, `SELECT key FROM kv WHERE ns=? AND key LIKE ? ORDER BY key`, ns, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}
