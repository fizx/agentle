package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kylemaxwell/agentle/internal/eval"
	"github.com/kylemaxwell/agentle/internal/platform"
	"github.com/kylemaxwell/agentle/internal/store"
)

// promoteGolden turns a run into a golden dataset entry for its script. The
// correctness label defaults to the run's up/down feedback (the ground truth)
// but can be set explicitly in the body.
func (s *Server) promoteGolden(w http.ResponseWriter, r *http.Request) {
	exe := s.execIfVisible(w, r, chi.URLParam(r, "id"))
	if exe == nil {
		return
	}
	var body struct {
		Label string `json:"label"`
		Note  string `json:"note"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	label := body.Label
	if label == "" {
		label = store.LabelFromFeedback(exe.Feedback) // "" feedback => success
	}
	if label != store.GoldenSuccess && label != store.GoldenFailure {
		httpError(w, http.StatusBadRequest, "label must be \"success\" or \"failure\"")
		return
	}
	g := store.Golden{
		ID:            "gold_" + uuid.NewString(),
		ScriptID:      exe.ScriptID,
		OriginExec:    exe.ID,
		OriginVersion: exe.Version,
		Label:         label,
		Note:          body.Note,
	}
	if err := s.svc.Store.CreateGolden(r.Context(), g); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) listGoldens(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.canEditScript(w, r, id) {
		return
	}
	goldens, err := s.svc.Store.ListGoldens(r.Context(), id)
	writeOrErr(w, goldens, err)
}

// calibrate measures judge↔human agreement over a script's golden dataset, so an
// operator can see whether to trust verdicts before relying on them.
func (s *Server) calibrate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.canEditScript(w, r, id) {
		return
	}
	stats, err := s.svc.CalibrateJudge(r.Context(), id, r.URL.Query().Get("model"))
	writeOrErr(w, stats, err)
}

// goldenIfVisible loads a golden and enforces edit rights on its script.
func (s *Server) goldenIfVisible(w http.ResponseWriter, r *http.Request, id string) *store.Golden {
	g, err := s.svc.Store.GetGolden(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "golden not found")
		return nil
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if !s.canEditScript(w, r, g.ScriptID) {
		return nil
	}
	return g
}

func (s *Server) deleteGolden(w http.ResponseWriter, r *http.Request) {
	if s.goldenIfVisible(w, r, chi.URLParam(r, "id")) == nil {
		return
	}
	if err := s.svc.Store.DeleteGolden(r.Context(), chi.URLParam(r, "id")); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateGoldenArtifacts saves the persona/criteria markdown authored in the UI
// (the simulator + judge artifacts of phases 3-4).
func (s *Server) updateGoldenArtifacts(w http.ResponseWriter, r *http.Request) {
	if s.goldenIfVisible(w, r, chi.URLParam(r, "id")) == nil {
		return
	}
	var body struct {
		Persona  string `json:"persona"`
		Criteria string `json:"criteria"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.svc.Store.UpdateGoldenArtifacts(r.Context(), chi.URLParam(r, "id"), body.Persona, body.Criteria); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runEval re-runs a target version against a golden and returns the coverage +
// boundary result. Query: ?version=N&allow_reads=1&miss=fail|go_live|flag&max_steps=N.
func (s *Server) runEval(w http.ResponseWriter, r *http.Request) {
	g := s.goldenIfVisible(w, r, chi.URLParam(r, "id"))
	if g == nil {
		return
	}
	q := r.URL.Query()
	version, _ := strconv.ParseUint(q.Get("version"), 10, 64)
	req := platform.EvalRequest{
		GoldenID:   g.ID,
		Version:    version,
		AllowReads: q.Get("allow_reads") == "1" || q.Get("allow_reads") == "true",
		MissPolicy: parseMissPolicy(q.Get("miss")),
		Judge:      q.Get("judge") == "1" || q.Get("judge") == "true",
		JudgeModel: q.Get("judge_model"),
		Mode:       q.Get("mode"),
	}
	if n, _ := strconv.Atoi(q.Get("max_steps")); n > 0 {
		req.Budget.MaxSteps = n
	}
	// samples>1 scores pass@k over the non-deterministic live LLM and returns a
	// suite; the single-sample path returns one EvalResult (back-compatible).
	if k, _ := strconv.Atoi(q.Get("samples")); k > 1 {
		suite, err := s.svc.RunEvalSamples(r.Context(), req, k)
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, http.StatusNotFound, "golden or version not found")
			return
		}
		writeOrErr(w, suite, err)
		return
	}
	res, err := s.svc.RunEval(r.Context(), req)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "golden or version not found")
		return
	}
	writeOrErr(w, res, err)
}

// --- tool_policy (operator read/write classification) ----------------------

func (s *Server) listToolPolicies(w http.ResponseWriter, r *http.Request) {
	policies, err := s.svc.Store.ListToolPolicies(r.Context())
	writeOrErr(w, policies, err)
}

func (s *Server) putToolPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) { // egress classification is an operator-level control
		return
	}
	var tp store.ToolPolicy
	if !readJSON(w, r, &tp) {
		return
	}
	if tp.Server == "" || tp.Tool == "" {
		httpError(w, http.StatusBadRequest, "server and tool required")
		return
	}
	tp.Source = store.PolicyOperator
	if err := s.svc.Store.PutToolPolicy(r.Context(), tp); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tp)
}

func (s *Server) deleteToolPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if err := s.svc.Store.DeleteToolPolicy(r.Context(), r.URL.Query().Get("server"), r.URL.Query().Get("tool")); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkConsistency runs the golden's origin version through its persona and
// reports whether it reproduces the recorded outcome (the gate before trusting
// the persona).
func (s *Server) checkConsistency(w http.ResponseWriter, r *http.Request) {
	g := s.goldenIfVisible(w, r, chi.URLParam(r, "id"))
	if g == nil {
		return
	}
	cr, err := s.svc.CheckPersonaConsistency(r.Context(), g.ID, r.URL.Query().Get("model"))
	writeOrErr(w, cr, err)
}

// draftPersona autofills a persona.md draft from the golden's recorded transcript.
// It is returned for the human to review/edit — never auto-saved.
func (s *Server) draftPersona(w http.ResponseWriter, r *http.Request) {
	g := s.goldenIfVisible(w, r, chi.URLParam(r, "id"))
	if g == nil {
		return
	}
	md, err := s.svc.DraftPersona(r.Context(), g.ID, r.URL.Query().Get("model"))
	writeOrErr(w, map[string]string{"persona": md}, err)
}

func parseMissPolicy(s string) eval.WriteMissPolicy {
	switch s {
	case "go_live":
		return eval.MissGoLive
	case "flag":
		return eval.MissFlag
	default:
		return eval.MissFail
	}
}
