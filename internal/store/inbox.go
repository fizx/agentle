package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Inbox is a durable per-workspace message queue backing the actor model:
// send() enqueues; recv()/yield consumes the oldest unconsumed message.
type Inbox struct{ db *sql.DB }

// Inbox returns the message queue over this database.
func (s *Store) Inbox() *Inbox { return &Inbox{db: s.db} }

// Enqueue appends a message to a workspace's inbox.
func (i *Inbox) Enqueue(ctx context.Context, workspace string, data json.RawMessage) error {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	_, err := i.db.ExecContext(ctx,
		`INSERT INTO inbox(workspace, data, created_at) VALUES(?,?,?)`,
		workspace, string(data), now())
	return err
}

// Claim atomically consumes the oldest unconsumed message for workspace, tagging
// it with idemKey. It is idempotent: a retry with the same idemKey returns the
// same message rather than consuming another — this is what makes recv crash-safe
// across replay. Returns ok=false when the inbox is empty.
func (i *Inbox) Claim(ctx context.Context, workspace, idemKey string) (json.RawMessage, bool, error) {
	tx, err := i.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	// Already claimed by this exact call (replay/retry)? Return that message.
	var data string
	err = tx.QueryRowContext(ctx,
		`SELECT data FROM inbox WHERE workspace=? AND consumed_by=? ORDER BY id LIMIT 1`,
		workspace, idemKey).Scan(&data)
	if err == nil {
		return json.RawMessage(data), true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	// Otherwise claim the oldest unconsumed message.
	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id, data FROM inbox WHERE workspace=? AND consumed_by='' ORDER BY id LIMIT 1`,
		workspace).Scan(&id, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, tx.Commit()
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inbox SET consumed_by=? WHERE id=?`, idemKey, id); err != nil {
		return nil, false, err
	}
	return json.RawMessage(data), true, tx.Commit()
}

// InboxDepth reports the number of unconsumed messages (for UI/diagnostics).
func (i *Inbox) InboxDepth(ctx context.Context, workspace string) (int, error) {
	var n int
	err := i.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox WHERE workspace=? AND consumed_by=''`, workspace).Scan(&n)
	return n, err
}
