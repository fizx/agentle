package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kylemaxwell/agentle/internal/store"
)

// VaultConfig configures the HashiCorp Vault KV v2 provider.
type VaultConfig struct {
	Addr   string // e.g. https://vault.internal:8200
	Token  string // Vault token (from env/role — never stored in our DB)
	Mount  string // KV v2 mount (default "secret")
	Prefix string // path prefix under the mount (default "agentle")
	Client *http.Client
}

// vaultStore is a Store backed by Vault KV v2. Each secret lives at
// {mount}/data/{prefix}/{scope}/{name} with a single "value" field.
type vaultStore struct {
	addr, token, mount, prefix string
	client                     *http.Client
}

// Vault returns a Vault-backed secret Store. Addr and Token are required.
func Vault(cfg VaultConfig) (Store, error) {
	if cfg.Addr == "" || cfg.Token == "" {
		return nil, fmt.Errorf("vault: addr and token are required")
	}
	if cfg.Mount == "" {
		cfg.Mount = "secret"
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "agentle"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	return &vaultStore{
		addr:  strings.TrimRight(cfg.Addr, "/"),
		token: cfg.Token, mount: cfg.Mount, prefix: cfg.Prefix, client: cfg.Client,
	}, nil
}

func (v *vaultStore) Resolve(ctx context.Context, name, scriptID string) (string, error) {
	if val, err := v.get(ctx, store.ScriptScope(scriptID), name); err == nil {
		return val, nil
	} else if err != store.ErrNotFound {
		return "", err
	}
	return v.get(ctx, store.ScopeGlobal, name)
}

func (v *vaultStore) Put(ctx context.Context, name, scope, value string) error {
	body, _ := json.Marshal(map[string]any{"data": map[string]any{"value": value}})
	_, err := v.do(ctx, http.MethodPost, v.dataPath(scope, name), body)
	return err
}

func (v *vaultStore) Delete(ctx context.Context, name, scope string) error {
	// Remove all versions + metadata.
	_, err := v.do(ctx, http.MethodDelete, v.metaPath(scope+"/"+name), nil)
	return err
}

func (v *vaultStore) ListNames(ctx context.Context, scope string) ([]string, error) {
	data, err := v.do(ctx, "LIST", v.metaPath(scope), nil)
	if err == store.ErrNotFound {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var body struct {
		Data struct {
			Keys []string `json:"keys"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	return body.Data.Keys, nil
}

func (v *vaultStore) Exists(ctx context.Context, name, scope string) (bool, error) {
	_, err := v.get(ctx, scope, name)
	if err == store.ErrNotFound {
		return false, nil
	}
	return err == nil, err
}

func (v *vaultStore) get(ctx context.Context, scope, name string) (string, error) {
	data, err := v.do(ctx, http.MethodGet, v.dataPath(scope, name), nil)
	if err != nil {
		return "", err
	}
	var body struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return "", err
	}
	val, ok := body.Data.Data["value"]
	if !ok {
		return "", store.ErrNotFound
	}
	return val, nil
}

// pathScope makes a scope safe as a Vault path segment ("script:<id>" -> "script_<id>").
func pathScope(scope string) string { return strings.ReplaceAll(scope, ":", "_") }

func (v *vaultStore) dataPath(scope, name string) string {
	return fmt.Sprintf("%s/data/%s/%s/%s", v.mount, v.prefix, pathScope(scope), name)
}
func (v *vaultStore) metaPath(suffix string) string {
	return fmt.Sprintf("%s/metadata/%s/%s", v.mount, v.prefix, pathScope(suffix))
}

// do performs a Vault API request, mapping 404 to store.ErrNotFound.
func (v *vaultStore) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, v.addr+"/v1/"+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", v.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, store.ErrNotFound
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("vault: %s %s -> %d: %s", method, path, resp.StatusCode, truncate(string(data), 200))
	}
	return data, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
