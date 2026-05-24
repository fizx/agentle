package store

import (
	"context"
	"fmt"
)

// UsageRow records the token usage + USD cost of one LLM call within an execution.
// It denormalizes script/workspace/owner so spend can be rolled up at every level
// without joins. Cost is priced at completion time (a historical snapshot).
type UsageRow struct {
	Exec         string  `json:"exec"`
	Seq          int64   `json:"seq"`
	ScriptID     string  `json:"script_id"`
	Workspace    string  `json:"workspace"`
	Owner        string  `json:"owner"`
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CacheTokens  int     `json:"cache_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	CreatedAt    int64   `json:"created_at"`
}

// PutUsage upserts a usage row keyed by (exec, seq) so re-recording on replay is
// idempotent.
func (s *Store) PutUsage(ctx context.Context, u UsageRow) error {
	if u.CreatedAt == 0 {
		u.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO usage(exec,seq,script_id,workspace,owner,model,input_tokens,output_tokens,cache_tokens,cost_usd,created_at)
         VALUES(?,?,?,?,?,?,?,?,?,?,?)
         ON CONFLICT(exec,seq) DO UPDATE SET model=excluded.model, input_tokens=excluded.input_tokens,
           output_tokens=excluded.output_tokens, cache_tokens=excluded.cache_tokens, cost_usd=excluded.cost_usd`,
		u.Exec, u.Seq, u.ScriptID, u.Workspace, u.Owner, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheTokens, u.CostUSD, u.CreatedAt)
	return err
}

// SpendRow is one rollup bucket.
type SpendRow struct {
	Key          string  `json:"key"`
	Calls        int     `json:"calls"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// spendDimensions maps an API dimension name to its column (allowlist — never
// interpolate caller input into SQL).
var spendDimensions = map[string]string{
	"script":    "script_id",
	"workspace": "workspace",
	"user":      "owner",
	"model":     "model",
	"exec":      "exec",
}

// Spend rolls up usage by dimension (script|workspace|user|model|exec), optionally
// restricted to an owner (RBAC) and to rows since a unix-nanos timestamp.
func (s *Store) Spend(ctx context.Context, dimension, owner string, since int64) ([]SpendRow, error) {
	col, ok := spendDimensions[dimension]
	if !ok {
		return nil, fmt.Errorf("unknown spend dimension %q", dimension)
	}
	q := `SELECT ` + col + ` AS key, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost_usd),0)
          FROM usage WHERE created_at>=?`
	args := []any{since}
	if owner != "" {
		q += ` AND owner=?`
		args = append(args, owner)
	}
	q += ` GROUP BY ` + col + ` ORDER BY SUM(cost_usd) DESC, COUNT(*) DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpendRow{}
	for rows.Next() {
		var r SpendRow
		if err := rows.Scan(&r.Key, &r.Calls, &r.InputTokens, &r.OutputTokens, &r.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
