package api

import "net/http"

// systemRoutes registers Agents, System, Push and Notifications endpoints (docs/API_SPEC.md).
func (s *Server) systemRoutes(mux *http.ServeMux) {
	_ = mux
}
