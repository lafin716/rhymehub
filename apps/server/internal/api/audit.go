package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
)

const defaultAuditLimit = 100

// handleListAudit serves GET /audit?limit=&before= (admin only, see routes). Entries are newest first. before is
// an audit id: only entries with a smaller id are returned, which is how the next page is requested.
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request, _ *principal) {
	limit, ok := parseLimit(w, r.URL.Query().Get("limit"), defaultAuditLimit)
	if !ok {
		return
	}
	var beforeID int64
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			writeDomainError(w, badRequest("before must be a positive audit id"))
			return
		}
		beforeID = n
	}
	entries, err := s.st.ListAudit(r.Context(), limit, beforeID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if entries == nil {
		entries = []domain.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
