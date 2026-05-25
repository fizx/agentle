package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kylemaxwell/agentle/internal/platform"
	"github.com/kylemaxwell/agentle/internal/store"
)

// codingAssistantScript is the seeded harness that powers the in-editor agent
// panel. Each chat is one execution of it, bound to a per-(script,chat) workspace.
const codingAssistantScript = "sc_coding_assistant"

// chatWorkspace is the actor/workspace a chat's harness run is bound to, so the
// chat resumes (same kv + inbox) across editor reopens.
func chatWorkspace(scriptID, chatID string) string {
	return "chat:" + scriptID + ":" + chatID
}

// listChats returns the editor's chat tabs for a script.
func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	scriptID := chi.URLParam(r, "id")
	if !s.canEditScript(w, r, scriptID) {
		return
	}
	chats, err := s.svc.Store.ListChats(r.Context(), scriptID)
	writeOrErr(w, chats, err)
}

// createChat opens a new agent chat for a script: it starts a harness execution
// bound to the chat's workspace (parked on recv()), then records the tab.
func (s *Server) createChat(w http.ResponseWriter, r *http.Request) {
	scriptID := chi.URLParam(r, "id")
	if !s.canEditScript(w, r, scriptID) {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	_ = readJSON(w, r, &body) // title optional

	chatID := "ch_" + uuid.NewString()
	exe, err := s.svc.RunExecution(r.Context(), platform.RunRequest{
		ScriptID:      codingAssistantScript,
		Kind:          "dashboard",
		ActorTemplate: chatWorkspace(scriptID, chatID),
	})
	if err != nil {
		httpError(w, http.StatusInternalServerError, "start assistant: "+err.Error())
		return
	}
	title := body.Title
	if title == "" {
		title = "New chat"
	}
	c := store.Chat{ID: chatID, ScriptID: scriptID, ExecID: exe.ID, Title: title}
	if err := s.svc.Store.PutChat(r.Context(), c); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	got, err := s.svc.Store.GetChat(r.Context(), chatID)
	writeOrErr(w, got, err)
}

// updateChat renames a chat (its tab title).
func (s *Server) updateChat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.chatForEdit(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Title != "" {
		c.Title = body.Title
	}
	if err := s.svc.Store.PutChat(r.Context(), *c); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// deleteChat closes a chat tab. The backing execution stays in run history.
func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.chatForEdit(w, r)
	if !ok {
		return
	}
	if err := s.svc.Store.DeleteChat(r.Context(), c.ID); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": c.ID})
}

// chatForEdit loads the chat named in the path and checks edit rights on its
// script.
func (s *Server) chatForEdit(w http.ResponseWriter, r *http.Request) (*store.Chat, bool) {
	c, err := s.svc.Store.GetChat(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpError(w, http.StatusNotFound, "chat not found")
		return nil, false
	}
	if !s.canEditScript(w, r, c.ScriptID) {
		return nil, false
	}
	return c, true
}
