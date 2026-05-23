package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/engine"
	"github.com/kylemaxwell/agentle/internal/platform"
	"github.com/kylemaxwell/agentle/internal/store"
)

// The public REST API (/v1) is the programmatic surface for integrators. Unlike
// the dashboard control plane (trusted X-Agentle-User header), it authenticates
// with a Bearer API token that resolves to a user and carries that user's RBAC.

// bearerIdentity authenticates a /v1 request from its Authorization: Bearer token
// and stashes the resolved user on the context (same key the shared handlers read).
func (s *Server) bearerIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plaintext := bearerToken(r)
		if plaintext == "" {
			httpError(w, http.StatusUnauthorized, "missing Bearer token")
			return
		}
		tok, err := s.svc.Store.ResolveAPIToken(r.Context(), plaintext)
		if err != nil {
			httpError(w, http.StatusUnauthorized, "invalid API token")
			return
		}
		u, err := s.svc.Store.GetUser(r.Context(), tok.UserID)
		if err != nil {
			httpError(w, http.StatusUnauthorized, "token user no longer exists")
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// runView is the public, stable shape for an execution: it adds a human-readable
// status alongside the numeric one so integrators don't hard-code the enum.
type runView struct {
	ID        string          `json:"id"`
	ScriptID  string          `json:"script_id"`
	Version   uint64          `json:"version"`
	Workspace string          `json:"workspace"`
	Status    string          `json:"status"`
	StatusNum int             `json:"status_code"`
	Output    json.RawMessage `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

func toRunView(e *store.Execution) runView {
	return runView{
		ID: e.ID, ScriptID: e.ScriptID, Version: e.Version, Workspace: e.ActorID,
		Status: engine.Status(e.Status).String(), StatusNum: e.Status,
		Output: e.Output, Error: e.Error, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

// v1Run starts a run of a script and returns its execution (which may be
// completed, failed, or suspended). POST /v1/scripts/{id}/runs.
func (s *Server) v1Run(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.canEditScript(w, r, id) {
		return
	}
	var body struct {
		Input   json.RawMessage `json:"input"`
		Version uint64          `json:"version"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Input) == 0 {
		body.Input = json.RawMessage("null")
	}
	exe, err := s.svc.RunExecution(r.Context(), platform.RunRequest{
		ScriptID: id, Version: body.Version, Kind: "api", Data: body.Input,
	})
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRunView(exe))
}

// v1GetRun returns one execution. GET /v1/runs/{id}.
func (s *Server) v1GetRun(w http.ResponseWriter, r *http.Request) {
	exe := s.execIfVisible(w, r, chi.URLParam(r, "id"))
	if exe == nil {
		return
	}
	writeJSON(w, http.StatusOK, toRunView(exe))
}

// v1GetTrace returns an execution's trace. GET /v1/runs/{id}/trace.
func (s *Server) v1GetTrace(w http.ResponseWriter, r *http.Request) {
	if s.execIfVisible(w, r, chi.URLParam(r, "id")) == nil {
		return
	}
	tr, err := s.svc.GetTrace(r.Context(), chi.URLParam(r, "id"))
	writeOrErr(w, tr, err)
}

// --- token management (control plane: /api/tokens) -------------------------

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	toks, err := s.svc.Store.ListAPITokens(r.Context(), currentUser(r.Context()).ID)
	writeOrErr(w, toks, err)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	tok, err := s.svc.Store.CreateAPIToken(r.Context(), "tok_"+uuid.NewString(), body.Name, currentUser(r.Context()).ID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The plaintext token is returned exactly once, here.
	writeJSON(w, http.StatusCreated, tok)
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r.Context())
	owner := u.ID
	if u.Role == store.RoleAdmin {
		owner = "" // admins can revoke any token
	}
	if err := s.svc.Store.DeleteAPIToken(r.Context(), chi.URLParam(r, "id"), owner); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
