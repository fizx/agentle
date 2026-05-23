package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// APIToken is a bearer credential for the programmatic REST API, bound to a user
// (whose RBAC it carries). Only a SHA-256 hash of the secret is stored; the
// plaintext is shown once at creation and never again.
type APIToken struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	UserID     string `json:"user_id"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	// Token is the plaintext secret — populated only by CreateAPIToken, never read back.
	Token string `json:"token,omitempty"`
}

// tokenPrefix namespaces agentle API tokens so they're recognizable in logs/UIs.
const tokenPrefix = "agtl_"

func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// CreateAPIToken mints a new token for a user and stores only its hash. The
// returned APIToken carries the plaintext in Token — surface it to the caller
// exactly once.
func (s *Store) CreateAPIToken(ctx context.Context, id, name, userID string) (*APIToken, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	plaintext := tokenPrefix + hex.EncodeToString(buf)
	tok := &APIToken{ID: id, Name: name, UserID: userID, CreatedAt: now(), Token: plaintext}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_tokens(id,hash,name,user_id,created_at,last_used_at) VALUES(?,?,?,?,?,0)`,
		tok.ID, hashToken(plaintext), tok.Name, tok.UserID, tok.CreatedAt)
	if err != nil {
		return nil, err
	}
	return tok, nil
}

// ResolveAPIToken looks up the token by its plaintext (via hash) and records
// last-used. Returns ErrNotFound if unknown.
func (s *Store) ResolveAPIToken(ctx context.Context, plaintext string) (*APIToken, error) {
	var tok APIToken
	err := s.db.QueryRowContext(ctx,
		`SELECT id,name,user_id,created_at,last_used_at FROM api_tokens WHERE hash=?`, hashToken(plaintext)).
		Scan(&tok.ID, &tok.Name, &tok.UserID, &tok.CreatedAt, &tok.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, now(), tok.ID)
	return &tok, nil
}

// ListAPITokens returns a user's tokens (never hashes or plaintext).
func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,name,user_id,created_at,last_used_at FROM api_tokens WHERE user_id=? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var tok APIToken
		if err := rows.Scan(&tok.ID, &tok.Name, &tok.UserID, &tok.CreatedAt, &tok.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes a token. Scoping to userID (non-empty) ensures a user
// can only revoke their own; pass "" for an admin revoking any.
func (s *Store) DeleteAPIToken(ctx context.Context, id, userID string) error {
	if userID != "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=? AND user_id=?`, id, userID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id=?`, id)
	return err
}
