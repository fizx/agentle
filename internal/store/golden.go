package store

import (
	"context"
	"database/sql"
	"errors"
)

// Golden label values (correctness, sourced from a run's up/down feedback). A
// failure-tagged golden inverts the eval's success semantics: the new version
// passes by *avoiding* the recorded failure.
const (
	GoldenSuccess = "success"
	GoldenFailure = "failure"
)

// Golden is one promoted run in a script's golden dataset. It is decoupled from
// any version's internal trajectory: the HTTP cassette and recorded user inputs
// are rebuilt from the origin execution's event log at eval time, so the golden
// does not rot when the script changes. Persona/Criteria are authored artifacts
// (PLAYTEST7 phases 3-4), stored inline; empty until those phases populate them.
type Golden struct {
	ID            string `json:"id"`
	ScriptID      string `json:"script_id"`
	OriginExec    string `json:"origin_exec"`        // execution the golden was promoted from
	OriginVersion uint64 `json:"origin_version"`     // for the self-consistency gate
	Label         string `json:"label"`              // "success" | "failure"
	Persona       string `json:"persona,omitempty"`  // persona.md (phase 4)
	Criteria      string `json:"criteria,omitempty"` // criteria.md (phase 3)
	Note          string `json:"note,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

// LabelFromFeedback maps a run's up/down vote to a golden correctness label.
func LabelFromFeedback(feedback string) string {
	if feedback == FeedbackDown {
		return GoldenFailure
	}
	return GoldenSuccess
}

// CreateGolden inserts a golden into a script's dataset.
func (s *Store) CreateGolden(ctx context.Context, g Golden) error {
	if g.CreatedAt == 0 {
		g.CreatedAt = now()
	}
	if g.Label == "" {
		g.Label = GoldenSuccess
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO goldens(id,script_id,origin_exec,origin_version,label,persona,criteria,note,created_at)
         VALUES(?,?,?,?,?,?,?,?,?)`,
		g.ID, g.ScriptID, g.OriginExec, g.OriginVersion, g.Label, g.Persona, g.Criteria, g.Note, g.CreatedAt)
	return err
}

func (s *Store) GetGolden(ctx context.Context, id string) (*Golden, error) {
	var g Golden
	err := s.db.QueryRowContext(ctx,
		`SELECT id,script_id,origin_exec,origin_version,label,persona,criteria,note,created_at FROM goldens WHERE id=?`, id).
		Scan(&g.ID, &g.ScriptID, &g.OriginExec, &g.OriginVersion, &g.Label, &g.Persona, &g.Criteria, &g.Note, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &g, err
}

// ListGoldens returns a script's golden dataset, newest first.
func (s *Store) ListGoldens(ctx context.Context, scriptID string) ([]Golden, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,script_id,origin_exec,origin_version,label,persona,criteria,note,created_at
         FROM goldens WHERE script_id=? ORDER BY created_at DESC`, scriptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Golden{}
	for rows.Next() {
		var g Golden
		if err := rows.Scan(&g.ID, &g.ScriptID, &g.OriginExec, &g.OriginVersion, &g.Label, &g.Persona, &g.Criteria, &g.Note, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// UpdateGoldenArtifacts sets the persona/criteria text on a golden (authored in
// the UI). Empty strings clear them.
func (s *Store) UpdateGoldenArtifacts(ctx context.Context, id, persona, criteria string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE goldens SET persona=?, criteria=? WHERE id=?`, persona, criteria, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGolden removes a golden from its dataset.
func (s *Store) DeleteGolden(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM goldens WHERE id=?`, id)
	return err
}
