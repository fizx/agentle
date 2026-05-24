package secrets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kylemaxwell/agentle/internal/store"
)

// exerciseStore runs the provider-agnostic contract.
func exerciseStore(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()

	if err := s.Put(ctx, "K", store.ScopeGlobal, "g"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "K", store.ScriptScope("s1"), "s"); err != nil {
		t.Fatal(err)
	}
	// Script scope wins over global; unknown script falls back to global.
	if got, _ := s.Resolve(ctx, "K", "s1"); got != "s" {
		t.Fatalf("Resolve(K,s1) = %q, want s", got)
	}
	if got, _ := s.Resolve(ctx, "K", "other"); got != "g" {
		t.Fatalf("Resolve(K,other) = %q, want g", got)
	}
	if ok, _ := s.Exists(ctx, "K", store.ScopeGlobal); !ok {
		t.Fatal("K should exist in global")
	}
	if ok, _ := s.Exists(ctx, "missing", store.ScopeGlobal); ok {
		t.Fatal("missing should not exist")
	}
	if names, _ := s.ListNames(ctx, store.ScopeGlobal); len(names) != 1 || names[0] != "K" {
		t.Fatalf("ListNames(global) = %v, want [K]", names)
	}
	if err := s.Delete(ctx, "K", store.ScopeGlobal); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Exists(ctx, "K", store.ScopeGlobal); ok {
		t.Fatal("K should be gone after delete")
	}
}

func TestSQLiteProvider(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	exerciseStore(t, SQLite(st))
}

func TestVaultProvider(t *testing.T) {
	srv := mockVault()
	defer srv.Close()
	v, err := Vault(VaultConfig{Addr: srv.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	exerciseStore(t, v)
}

// mockVault is a tiny in-memory KV v2 server (data/list/delete) for the provider test.
func mockVault() *httptest.Server {
	var mu sync.Mutex
	data := map[string]string{} // key: "<scopePath>/<name>"
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/v1/secret/")
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(p, "data/agentle/"):
			var body struct {
				Data struct {
					Value string `json:"value"`
				} `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			data[strings.TrimPrefix(p, "data/agentle/")] = body.Data.Value
			mu.Unlock()
			_, _ = w.Write([]byte(`{"data":{}}`))
		case r.Method == http.MethodGet && strings.HasPrefix(p, "data/agentle/"):
			mu.Lock()
			v, ok := data[strings.TrimPrefix(p, "data/agentle/")]
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"value": v}}})
		case r.Method == "LIST" && strings.HasPrefix(p, "metadata/agentle/"):
			scope := strings.TrimPrefix(p, "metadata/agentle/")
			var keys []string
			mu.Lock()
			for k := range data {
				if strings.HasPrefix(k, scope+"/") {
					keys = append(keys, strings.TrimPrefix(k, scope+"/"))
				}
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"keys": keys}})
		case r.Method == http.MethodDelete && strings.HasPrefix(p, "metadata/agentle/"):
			mu.Lock()
			delete(data, strings.TrimPrefix(p, "metadata/agentle/"))
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}
