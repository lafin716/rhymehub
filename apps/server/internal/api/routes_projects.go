package api

import (
	"net/http"

	"github.com/lafin716/rhymehub/internal/domain"
)

// projectRoutes registers Projects, Releases and Audit endpoints (docs/API_SPEC.md).
// Role minimums are noted per route; "" means any authenticated user.
func (s *Server) projectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects", s.need("", s.handleListProjects))
	mux.HandleFunc("POST /api/v1/projects", s.need(domain.RoleDeveloper, s.handleCreateProject))
	mux.HandleFunc("POST /api/v1/projects/discover", s.need(domain.RoleDeveloper, s.handleDiscoverProjects))
	mux.HandleFunc("GET /api/v1/projects/{id}", s.need("", s.handleGetProject))
	mux.HandleFunc("PATCH /api/v1/projects/{id}", s.need(domain.RoleDeveloper, s.handlePatchProject))
	mux.HandleFunc("GET /api/v1/projects/{id}/git", s.need("", s.handleProjectGit))
	mux.HandleFunc("GET /api/v1/projects/{id}/keys", s.need(domain.RoleMaintainer, s.handleListKeys))
	mux.HandleFunc("POST /api/v1/projects/{id}/keys", s.need(domain.RoleMaintainer, s.handleCreateKey))
	mux.HandleFunc("DELETE /api/v1/projects/{id}/keys/{keyId}", s.need(domain.RoleMaintainer, s.handleRevokeKey))
	mux.HandleFunc("GET /api/v1/projects/{id}/sdk-prompt", s.need("", s.handleSDKPrompt))
	mux.HandleFunc("POST /api/v1/projects/{id}/releases", s.need(domain.RoleMaintainer, s.handleManualRelease))
	mux.HandleFunc("GET /api/v1/releases", s.need("", s.handleListReleases))
	mux.HandleFunc("GET /api/v1/releases/{id}", s.need("", s.handleGetRelease))
	mux.HandleFunc("GET /api/v1/audit", s.need(domain.RoleAdmin, s.handleListAudit))
}
