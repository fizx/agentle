package store

import (
	"context"
	"database/sql"
	"errors"
)

// Chat is one coding-assistant conversation attached to a script. Its durable
// state lives in a harness execution (ExecID) bound to the workspace
// chat:{ScriptID}:{ID}; this row only tracks the tab's metadata (title, order,
// archived) so the editor can list/rename/close chats without scanning runs.
type Chat struct {
	ID        string `json:"id"`
	ScriptID  string `json:"script_id"`
	ExecID    string `json:"exec_id"`
	Title     string `json:"title"`
	Archived  bool   `json:"archived"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// PutChat upserts a chat by id, refreshing updated_at.
func (s *Store) PutChat(ctx context.Context, c Chat) error {
	if c.CreatedAt == 0 {
		c.CreatedAt = now()
	}
	c.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chats(id,script_id,exec_id,title,archived,created_at,updated_at) VALUES(?,?,?,?,?,?,?)
         ON CONFLICT(id) DO UPDATE SET exec_id=excluded.exec_id, title=excluded.title, archived=excluded.archived, updated_at=excluded.updated_at`,
		c.ID, c.ScriptID, c.ExecID, c.Title, boolInt(c.Archived), c.CreatedAt, c.UpdatedAt)
	return err
}

func (s *Store) GetChat(ctx context.Context, id string) (*Chat, error) {
	var c Chat
	var ar int
	err := s.db.QueryRowContext(ctx,
		`SELECT id,script_id,exec_id,title,archived,created_at,updated_at FROM chats WHERE id=?`, id).
		Scan(&c.ID, &c.ScriptID, &c.ExecID, &c.Title, &ar, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	c.Archived = ar != 0
	return &c, err
}

// ListChats returns a script's non-archived chats, oldest first (stable tab order).
func (s *Store) ListChats(ctx context.Context, scriptID string) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,script_id,exec_id,title,archived,created_at,updated_at FROM chats
         WHERE script_id=? AND archived=0 ORDER BY created_at`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Chat{}
	for rows.Next() {
		var c Chat
		var ar int
		if err := rows.Scan(&c.ID, &c.ScriptID, &c.ExecID, &c.Title, &ar, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Archived = ar != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteChat removes a chat row (the backing execution stays in run history).
func (s *Store) DeleteChat(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chats WHERE id=?`, id)
	return err
}
