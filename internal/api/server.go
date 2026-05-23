// Package api is the control-plane HTTP server: script/version CRUD, run, history,
// trace, secrets, tool configs, triggers, and inbound webhooks. It also serves the
// embedded dashboard.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/platform"
	"github.com/kylemaxwell/agentle/internal/store"
	"github.com/kylemaxwell/agentle/internal/trigger"
)

// Server hosts the control-plane API and dashboard.
type Server struct {
	svc    *platform.Service
	sched  *trigger.Scheduler
	static fs.FS
	log    *slog.Logger
}

func New(svc *platform.Service, sched *trigger.Scheduler, static fs.FS, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{svc: svc, sched: sched, static: static, log: log}
}

// Handler builds the HTTP router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(6 * time.Minute))

	r.Route("/api", func(r chi.Router) {
		r.Get("/scripts", s.listScripts)
		r.Post("/scripts", s.createScript)
		r.Get("/scripts/{id}", s.getScript)
		r.Get("/scripts/{id}/versions", s.listVersions)
		r.Post("/scripts/{id}/versions", s.saveVersion)
		r.Post("/scripts/{id}/run", s.runScript)

		r.Get("/executions", s.listExecutions)
		r.Get("/executions/{id}", s.getExecution)
		r.Get("/executions/{id}/trace", s.getTrace)

		r.Get("/configs", s.listConfigs)
		r.Put("/configs", s.putConfig)

		r.Get("/secrets", s.listSecrets)
		r.Put("/secrets", s.putSecret)

		r.Get("/triggers", s.listTriggers)
		r.Put("/triggers", s.putTrigger)
		r.Delete("/triggers/{id}", s.deleteTrigger)

		r.Post("/hooks/{token}", s.webhook)
	})

	if s.static != nil {
		s.mountStatic(r)
	}
	return r
}

// --- scripts ---------------------------------------------------------------

func (s *Server) listScripts(w http.ResponseWriter, r *http.Request) {
	scripts, err := s.svc.Store.ListScripts(r.Context())
	writeOrErr(w, scripts, err)
}

func (s *Server) createScript(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		httpError(w, http.StatusBadRequest, "name required")
		return
	}
	id := "sc_" + uuid.NewString()
	sc, err := s.svc.Store.CreateScript(r.Context(), id, body.Name)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Source != "" {
		if _, err := s.svc.Store.SaveVersion(r.Context(), id, body.Source, "", nil); err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, sc)
}

type scriptDetail struct {
	Script   *store.Script   `json:"script"`
	Versions []store.Version `json:"versions"`
}

func (s *Server) getScript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sc, err := s.svc.Store.GetScript(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "script not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	versions, err := s.svc.Store.ListVersions(r.Context(), id)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, scriptDetail{Script: sc, Versions: versions})
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.svc.Store.ListVersions(r.Context(), chi.URLParam(r, "id"))
	writeOrErr(w, versions, err)
}

func (s *Server) saveVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string           `json:"source"`
		Image  string           `json:"image"`
		Grants []store.GrantRef `json:"grants"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.svc.Store.SaveVersion(r.Context(), chi.URLParam(r, "id"), body.Source, body.Image, body.Grants)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "script not found")
		return
	}
	writeOrErr(w, v, err)
}

func (s *Server) runScript(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version uint64          `json:"version"`
		Input   json.RawMessage `json:"input"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Input) == 0 {
		body.Input = json.RawMessage("null")
	}
	exe, err := s.svc.RunExecution(r.Context(), chi.URLParam(r, "id"), body.Version, body.Input, "manual")
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "script not found")
		return
	}
	writeOrErr(w, exe, err)
}

// --- executions ------------------------------------------------------------

func (s *Server) listExecutions(w http.ResponseWriter, r *http.Request) {
	list, err := s.svc.Store.ListExecutions(r.Context(), r.URL.Query().Get("script"), 100)
	writeOrErr(w, list, err)
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	exe, err := s.svc.Store.GetExecution(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "execution not found")
		return
	}
	writeOrErr(w, exe, err)
}

func (s *Server) getTrace(w http.ResponseWriter, r *http.Request) {
	tr, err := s.svc.GetTrace(r.Context(), chi.URLParam(r, "id"))
	writeOrErr(w, tr, err)
}

// --- configs & secrets -----------------------------------------------------

func (s *Server) listConfigs(w http.ResponseWriter, r *http.Request) {
	configs, err := s.svc.Store.ListToolConfigs(r.Context())
	writeOrErr(w, configs, err)
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var c store.ToolConfig
	if !readJSON(w, r, &c) {
		return
	}
	if c.ID == "" || c.Capability == "" {
		httpError(w, http.StatusBadRequest, "id and capability required")
		return
	}
	if err := s.svc.Store.PutToolConfig(r.Context(), c); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": c.ID})
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	names, err := s.svc.Store.ListSecretNames(r.Context())
	writeOrErr(w, map[string]any{"names": names}, err)
}

func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		httpError(w, http.StatusBadRequest, "name required")
		return
	}
	if err := s.svc.Store.PutSecret(r.Context(), body.Name, body.Value); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": body.Name})
}

// --- triggers --------------------------------------------------------------

func (s *Server) listTriggers(w http.ResponseWriter, r *http.Request) {
	triggers, err := s.svc.Store.ListTriggers(r.Context(), r.URL.Query().Get("kind"))
	writeOrErr(w, triggers, err)
}

func (s *Server) putTrigger(w http.ResponseWriter, r *http.Request) {
	var t store.Trigger
	if !readJSON(w, r, &t) {
		return
	}
	if t.ID == "" {
		t.ID = "tr_" + uuid.NewString()
		t.Enabled = true // new triggers are enabled by default
	}
	if t.ScriptID == "" || (t.Kind != "cron" && t.Kind != "webhook") {
		httpError(w, http.StatusBadRequest, "script_id and kind (cron|webhook) required")
		return
	}
	if t.Kind == "webhook" && t.Spec == "" {
		t.Spec = uuid.NewString() // generate a webhook token
	}
	if err := s.svc.Store.PutTrigger(r.Context(), t); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadCron(r.Context())
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) deleteTrigger(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.Store.DeleteTrigger(r.Context(), chi.URLParam(r, "id")); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadCron(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	t, err := s.svc.Store.FindWebhookTrigger(r.Context(), token)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such webhook")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	input, _ := json.Marshal(map[string]any{
		"trigger": "webhook",
		"body":    string(bodyBytes),
		"query":   r.URL.RawQuery,
	})
	exe, err := s.svc.RunExecution(r.Context(), t.ScriptID, 0, input, "webhook:"+t.ID)
	writeOrErr(w, exe, err)
}

func (s *Server) reloadCron(ctx context.Context) {
	if s.sched == nil {
		return
	}
	if err := s.sched.Reload(ctx); err != nil {
		s.log.Warn("cron reload failed", "err", err)
	}
}
