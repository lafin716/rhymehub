package api

import (
	"net/http"

	"github.com/lafin716/rhymehub/internal/domain"
)

// projectRoutes registers Projects, Releases and Audit endpoints (docs/API_SPEC.md).
func (s *Server) projectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/projects", s.need("", s.handleListProjects))
	mux.HandleFunc("GET /api/v1/projects/{id}", s.need("", s.handleGetProject))
	_ = domain.RoleAdmin
}
