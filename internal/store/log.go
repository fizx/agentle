package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// SQLLog is a durable engine.Log backed by SQLite. Single-writer is enforced by
// the (exec, seq) primary key: an Append at an already-used seq fails the unique
// constraint and surfaces as engine.ErrConflict. Fencing is delegated to an
// optional FenceChecker (the leaser) for split-brain protection.
type SQLLog struct {
	db     *sql.DB
	fences engine.FenceChecker
}

// EventLog returns a durable Log over this store's database. fences may be nil.
func (s *Store) EventLog(fences engine.FenceChecker) *SQLLog {
	return &SQLLog{db: s.db, fences: fences}
}

func (l *SQLLog) Append(ctx context.Context, exec engine.ExecutionID, expectedSeq engine.Seq, token engine.FenceToken, ev engine.Event, _ bool) (engine.Seq, error) {
	if l.fences != nil {
		if cur, ok := l.fences.Current(exec); ok && token != cur {
			return 0, engine.ErrFenced
		}
	}
	ev.Seq = expectedSeq
	data, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	_, err = l.db.ExecContext(ctx, `INSERT INTO events(exec,seq,data) VALUES(?,?,?)`, string(exec), int64(expectedSeq), string(data))
	if err != nil {
		if isUniqueViolation(err) {
			return 0, engine.ErrConflict
		}
		return 0, err
	}
	return expectedSeq, nil
}

func (l *SQLLog) Read(ctx context.Context, exec engine.ExecutionID, fromSeq engine.Seq) ([]engine.Event, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT data FROM events WHERE exec=? AND seq>=? ORDER BY seq`, string(exec), int64(fromSeq))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []engine.Event
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var ev engine.Event
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	// modernc.org/sqlite surfaces constraint errors in the message.
	return strings.Contains(err.Error(), "constraint failed")
}
