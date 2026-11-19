package api

import (
	"net/http"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
)

const maxReleaseNotesBytes = maxTextBytes

// handleManualRelease serves POST /projects/{id}/releases. The release logic lives in ProjectReleaser; this layer
// only checks availability, drain state and input shape.
func (s *Server) handleManualRelease(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		Version string `json:"version"`
		Notes   string `json:"notes"`
	}
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	if s.projRel == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "manual releases are not configured")
		return
	}
	if s.Draining() {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "server is draining; try again after the update")
		return
	}
	if len(in.Notes) > maxReleaseNotesBytes {
		writeDomainError(w, badRequest("notes must be at most %d bytes", maxReleaseNotesBytes))
		return
	}
	version := strings.TrimSpace(in.Version)
	if len(version) > 64 {
		writeDomainError(w, badRequest("version must be at most 64 characters"))
		return
	}
	rel, err := s.projRel.ReleaseProject(r.Context(), r.PathValue("id"), version, in.Notes, p.User.ID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, rel)
}

// handleListReleases serves GET /releases?projectId=. The projectId parameter is required.
func (s *Server) handleListReleases(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	projectID := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if projectID == "" {
		writeDomainError(w, badRequest("projectId query parameter is required"))
		return
	}
	if _, err := s.st.GetProject(ctx, projectID); err != nil {
		writeDomainError(w, err)
		return
	}
	rels, err := s.st.ListReleases(ctx, projectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if rels == nil {
		rels = []domain.Release{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": rels})
}

// handleGetRelease serves GET /releases/{id}.
func (s *Server) handleGetRelease(w http.ResponseWriter, r *http.Request, _ *principal) {
	rel, err := s.st.GetRelease(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rel)
}
