package store

import (
	"context"
	"database/sql"
	"errors"
)

// Feedback labels. A run carries at most one pointwise human label, the ground
// truth the golden dataset's success/failure tag and the LLM judge's calibration
// corpus are both built from (PLAYTEST7 phase 0). "up" == success, "down" == fail.
const (
	FeedbackUp   = "up"
	FeedbackDown = "down"
)

// ValidFeedback reports whether label is a settable feedback value. The empty
// string is handled separately (it clears the label) and is not "valid" here.
func ValidFeedback(label string) bool { return label == FeedbackUp || label == FeedbackDown }

// Feedback is one reviewer's pointwise verdict on a run, keyed by execution id.
// Upsert semantics: the latest verdict wins (one row per execution).
type Feedback struct {
	Exec      string `json:"exec"`
	Label     string `json:"label"` // "up" | "down"
	Note      string `json:"note,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// SetFeedback upserts the label/note for an execution. An empty label clears any
// existing feedback (un-vote). createdAt is preserved across updates.
func (s *Store) SetFeedback(ctx context.Context, exec, label, note, userID string) error {
	if label == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM run_feedback WHERE exec=?`, exec)
		return err
	}
	ts := now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO run_feedback(exec,label,note,user_id,created_at,updated_at) VALUES(?,?,?,?,?,?)
         ON CONFLICT(exec) DO UPDATE SET label=excluded.label, note=excluded.note, user_id=excluded.user_id, updated_at=excluded.updated_at`,
		exec, label, note, userID, ts, ts)
	return err
}

// GetFeedback returns the feedback for an execution, or ErrNotFound if unvoted.
func (s *Store) GetFeedback(ctx context.Context, exec string) (*Feedback, error) {
	var f Feedback
	err := s.db.QueryRowContext(ctx,
		`SELECT exec,label,note,user_id,created_at,updated_at FROM run_feedback WHERE exec=?`, exec).
		Scan(&f.Exec, &f.Label, &f.Note, &f.UserID, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &f, err
}
