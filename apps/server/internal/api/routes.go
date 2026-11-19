package api

import (
	"net/http"

	"github.com/lafin716/rhymehub/apps/server/web"
	"github.com/lafin716/rhymehub/internal/domain"
)

// routes registers every endpoint. Minimum roles follow docs/API_SPEC.md: [any] = need(""),
// [dev] = need(RoleDeveloper), [maint] = need(RoleMaintainer), [admin] = need(RoleAdmin).
func (s *Server) routes(mux *http.ServeMux) {
	// Public.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.health())
	})
	mux.HandleFunc("GET /api/v1/meta", s.handleMeta)
	mux.HandleFunc("POST /api/v1/setup", s.handleSetup)

	// Auth.
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.need("", s.handleMe))

	// API tokens (any authenticated user manages its own tokens).
	mux.HandleFunc("GET /api/v1/tokens", s.need("", s.handleListTokens))
	mux.HandleFunc("POST /api/v1/tokens", s.need("", s.handleCreateToken))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.need("", s.handleRevokeToken))

	// Users (admin).
	mux.HandleFunc("GET /api/v1/users", s.need(domain.RoleAdmin, s.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", s.need(domain.RoleAdmin, s.handleCreateUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", s.need(domain.RoleAdmin, s.handlePatchUser))

	// Projects (read-only for now; create/patch/keys are not implemented yet).
	mux.HandleFunc("GET /api/v1/projects", s.need("", s.handleListProjects))
	mux.HandleFunc("GET /api/v1/projects/{id}", s.need("", s.handleGetProject))

	// Requests (inbox).
	mux.HandleFunc("GET /api/v1/requests", s.need("", s.handleListRequests))
	mux.HandleFunc("POST /api/v1/requests", s.need(domain.RoleDeveloper, s.handleCreateRequest))
	mux.HandleFunc("GET /api/v1/requests/{id}", s.need("", s.handleGetRequest))
	mux.HandleFunc("PATCH /api/v1/requests/{id}", s.need(domain.RoleDeveloper, s.handlePatchRequest))
	mux.HandleFunc("POST /api/v1/requests/{id}/task", s.need(domain.RoleDeveloper, s.handleCreateTaskFromRequest))

	// Tasks.
	mux.HandleFunc("GET /api/v1/tasks", s.need("", s.handleListTasks))
	mux.HandleFunc("POST /api/v1/tasks", s.need(domain.RoleDeveloper, s.handleCreateTask))
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.need("", s.handleGetTask))
	mux.HandleFunc("GET /api/v1/tasks/{id}/events", s.need("", s.handleTaskEvents))
	mux.HandleFunc("GET /api/v1/tasks/{id}/diff", s.need("", s.handleTaskDiff))
	mux.HandleFunc("POST /api/v1/tasks/{id}/messages", s.need(domain.RoleDeveloper, s.handleTaskMessage))
	mux.HandleFunc("POST /api/v1/tasks/{id}/answer", s.need(domain.RoleDeveloper, s.handleTaskAnswer))
	mux.HandleFunc("POST /api/v1/tasks/{id}/permission", s.need(domain.RoleDeveloper, s.handlePermission))
	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", s.need(domain.RoleDeveloper, s.handleTaskCancel))
	mux.HandleFunc("POST /api/v1/tasks/{id}/retry", s.need(domain.RoleDeveloper, s.handleTaskRetry))
	mux.HandleFunc("POST /api/v1/tasks/{id}/approve", s.need(domain.RoleMaintainer, s.handleApprove))
	mux.HandleFunc("POST /api/v1/tasks/{id}/reject", s.need(domain.RoleMaintainer, s.handleReject))

	// Live updates (SSE).
	mux.HandleFunc("GET /api/v1/events/stream", s.need("", s.handleEventStream))

	// Unknown API paths get a JSON 404 instead of the dashboard HTML.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})

	// Dashboard (embedded) with SPA index fallback.
	mux.Handle("/", web.Handler())
}
