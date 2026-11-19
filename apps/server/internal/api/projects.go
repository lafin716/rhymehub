package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
)

// projectStats are the per-project counters shown on the dashboard.
type projectStats struct {
	OpenRequests int `json:"openRequests"`
	ActiveTasks  int `json:"activeTasks"`
	ReviewReady  int `json:"reviewReady"`
	Failed       int `json:"failed"`
}

// projectView is the Project shape from docs/API_SPEC.md. The git block is not included yet.
type projectView struct {
	domain.Project
	Stats projectStats `json:"stats"`
}

func (s *Server) projectStatsFor(ctx context.Context, projectID string) (projectStats, error) {
	var st projectStats
	reqs, err := s.st.ListRequests(ctx, projectID, "", 0)
	if err != nil {
		return st, err
	}
	for _, rq := range reqs {
		if rq.Status != domain.RequestDone && rq.Status != domain.RequestRejected && rq.Status != domain.RequestFailed {
			st.OpenRequests++
		}
	}
	tasks, err := s.st.ListTasks(ctx, projectID, 0)
	if err != nil {
		return st, err
	}
	for _, t := range tasks {
		if domain.IsActive(t.State) {
			st.ActiveTasks++
		}
		switch t.State {
		case domain.StateReviewReady:
			st.ReviewReady++
		case domain.StateFailed:
			st.Failed++
		}
	}
	return st, nil
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request, _ *principal) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	projs, err := s.st.ListProjects(r.Context(), false)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]projectView, 0, len(projs))
	for _, p := range projs {
		if q != "" && !strings.Contains(strings.ToLower(p.Name), q) && !strings.Contains(strings.ToLower(p.ID), q) {
			continue
		}
		stats, err := s.projectStatsFor(r.Context(), p.ID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		out = append(out, projectView{Project: p, Stats: stats})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	p, err := s.st.GetProject(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	stats, err := s.projectStatsFor(ctx, p.ID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectView{Project: p, Stats: stats})
}
