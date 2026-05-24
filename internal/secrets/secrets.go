// Package secrets abstracts secret storage behind a Store interface so the
// backend is pluggable (SQLite by default, HashiCorp Vault as an external
// provider). Secrets are write-only from the control plane: values are consumed
// at the RPC boundary and never returned to scripts, traces, or read APIs.
package secrets

import (
	"context"

	"github.com/kylemaxwell/agentle/internal/store"
)

// Store is the pluggable secret backend. Scope is "global" or "script:<id>";
// Resolve prefers the script scope over global.
type Store interface {
	// Resolve returns the value of name for a script, preferring the script scope
	// over global. Returns an error (e.g. not found) when unset.
	Resolve(ctx context.Context, name, scriptID string) (string, error)
	// Put stores (overwrites) a value in a scope.
	Put(ctx context.Context, name, scope, value string) error
	// Delete removes a value from a scope.
	Delete(ctx context.Context, name, scope string) error
	// ListNames returns the names set in a scope — never values.
	ListNames(ctx context.Context, scope string) ([]string, error)
	// Exists reports whether name is set in a scope.
	Exists(ctx context.Context, name, scope string) (bool, error)
}

// sqliteStore adapts the SQLite-backed store.Store to the Store interface (the
// default provider).
type sqliteStore struct{ s *store.Store }

// SQLite returns the default secret backend over the data-model store.
func SQLite(s *store.Store) Store { return sqliteStore{s} }

func (a sqliteStore) Resolve(ctx context.Context, name, scriptID string) (string, error) {
	return a.s.ResolveSecret(ctx, name, scriptID)
}
func (a sqliteStore) Put(ctx context.Context, name, scope, value string) error {
	return a.s.PutSecret(ctx, name, scope, value)
}
func (a sqliteStore) Delete(ctx context.Context, name, scope string) error {
	return a.s.DeleteSecret(ctx, name, scope)
}
func (a sqliteStore) ListNames(ctx context.Context, scope string) ([]string, error) {
	return a.s.ListSecretNames(ctx, scope)
}
func (a sqliteStore) Exists(ctx context.Context, name, scope string) (bool, error) {
	return a.s.SecretExists(ctx, name, scope)
}
