package api

import (
	"context"
	"net/http"

	"github.com/kylemaxwell/agentle/internal/store"
)

// Identity is dev-mode: the dashboard sends the chosen user id in the
// X-Agentle-User header. There are no passwords yet — real auth (sessions/OAuth)
// is deferred. RBAC (admin > user) is enforced on top of the resolved identity.
const userHeader = "X-Agentle-User"

type ctxKey int

const userCtxKey ctxKey = iota

// identity resolves the current user and stashes it on the request context.
func (s *Server) identity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.resolveUser(r)
		ctx := context.WithValue(r.Context(), userCtxKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) resolveUser(r *http.Request) *store.User {
	if id := r.Header.Get(userHeader); id != "" {
		if u, err := s.svc.Store.GetUser(r.Context(), id); err == nil {
			return u
		}
	}
	// Default to the first admin so a fresh dev setup is usable.
	users, _ := s.svc.Store.ListUsers(r.Context())
	for i := range users {
		if users[i].Role == store.RoleAdmin {
			return &users[i]
		}
	}
	if len(users) > 0 {
		return &users[0]
	}
	return &store.User{ID: "dev", Name: "dev", Role: store.RoleAdmin}
}

func currentUser(ctx context.Context) *store.User {
	if u, ok := ctx.Value(userCtxKey).(*store.User); ok {
		return u
	}
	return &store.User{ID: "dev", Name: "dev", Role: store.RoleAdmin}
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if currentUser(r.Context()).Role != store.RoleAdmin {
		httpError(w, http.StatusForbidden, "admin role required")
		return false
	}
	return true
}

// canEditScript reports whether the current user owns the script or is admin.
func (s *Server) canEditScript(w http.ResponseWriter, r *http.Request, scriptID string) bool {
	u := currentUser(r.Context())
	if u.Role == store.RoleAdmin {
		return true
	}
	sc, err := s.svc.Store.GetScript(r.Context(), scriptID)
	if err != nil {
		httpError(w, http.StatusNotFound, "script not found")
		return false
	}
	if sc.Owner != u.ID {
		httpError(w, http.StatusForbidden, "not the script owner")
		return false
	}
	return true
}
