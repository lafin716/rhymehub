package api

import (
	"net/http"

	"github.com/lafin716/rhymehub/internal/domain"
)

// systemRoutes registers Agents, System, Push and Notifications endpoints (docs/API_SPEC.md).
func (s *Server) systemRoutes(mux *http.ServeMux) {
	// Agents.
	mux.HandleFunc("GET /api/v1/agents", s.need("", s.handleListAgents))
	mux.HandleFunc("PATCH /api/v1/agents/limits", s.need(domain.RoleAdmin, s.handlePatchAgentLimits))

	// System (self-update). Drain is authenticated by the supervisor token only, not by user sessions.
	mux.HandleFunc("GET /api/v1/system/status", s.need("", s.handleSystemStatus))
	mux.HandleFunc("GET /api/v1/system/updates", s.need("", s.handleSystemUpdates))
	mux.HandleFunc("POST /api/v1/system/update", s.need(domain.RoleAdmin, s.handleSystemUpdate))
	mux.HandleFunc("POST /api/v1/system/rollback", s.need(domain.RoleAdmin, s.handleSystemRollback))
	mux.HandleFunc("POST /api/v1/system/drain", s.handleSystemDrain)

	// Push and notifications.
	mux.HandleFunc("GET /api/v1/push/vapid-public-key", s.need("", s.handleVAPIDKey))
	mux.HandleFunc("POST /api/v1/push/subscribe", s.need("", s.handlePushSubscribe))
	mux.HandleFunc("DELETE /api/v1/push/subscribe", s.need("", s.handlePushUnsubscribe))
	mux.HandleFunc("POST /api/v1/push/test", s.need("", s.handlePushTest))
	mux.HandleFunc("GET /api/v1/notifications", s.need("", s.handleListNotifications))
	mux.HandleFunc("POST /api/v1/notifications/read", s.need("", s.handleMarkNotificationsRead))
}
