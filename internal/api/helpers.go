package api

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// pagination reads ?limit & ?offset query params with sane defaults.
func pagination(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	return limit, offset
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeOrErr writes v as 200 JSON, mapping a non-nil err to 500.
func writeOrErr(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// readJSON decodes the request body, writing a 400 on failure. Returns false if
// the caller should stop.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		httpError(w, http.StatusBadRequest, "read body: "+err.Error())
		return false
	}
	if len(data) == 0 {
		return true // allow empty body => zero-value struct
	}
	if err := json.Unmarshal(data, dst); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}

// mountStatic serves the embedded SPA: static assets where present, index.html
// fallback for client-side routes.
func (s *Server) mountStatic(r chi.Router) {
	index, err := fs.ReadFile(s.static, "index.html")
	hasIndex := err == nil
	fileServer := http.FileServer(http.FS(s.static))

	r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(s.static, path); err == nil {
			fileServer.ServeHTTP(w, req)
			return
		}
		if hasIndex {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		http.NotFound(w, req)
	})
}
