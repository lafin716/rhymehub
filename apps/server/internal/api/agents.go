package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/lafin716/rhymehub/internal/domain"
)

// settingSchedulerLimits is the settings key that stores AgentLimits as JSON. serve loads it at startup.
const settingSchedulerLimits = "scheduler.limits"

// maxLimitsBodyBytes bounds PATCH /agents/limits bodies.
const maxLimitsBodyBytes = 16 << 10

// agentView is one entry of GET /agents: the detected agent plus its running count and effective limit.
type agentView struct {
	Name         string            `json:"name"`
	Available    bool              `json:"available"`
	Version      string            `json:"version"`
	Path         string            `json:"path"`
	Capabilities AgentCapabilities `json:"capabilities"`
	Running      int               `json:"running"`
	Limit        int               `json:"limit"`
	Error        string            `json:"error,omitempty"`
}

// currentLimits returns the scheduler limits, or zero limits when no scheduler is configured.
func (s *Server) currentLimits() AgentLimits {
	if s.sched == nil {
		return AgentLimits{PerAgent: map[string]int{}}
	}
	lim := s.sched.CurrentLimits()
	if lim.PerAgent == nil {
		lim.PerAgent = map[string]int{}
	}
	return lim
}

// runningPerAgent counts PREPARING, RUNNING and VALIDATING tasks by agent name.
func (s *Server) runningPerAgent(ctx context.Context) (map[string]int, error) {
	tasks, err := s.st.ListTasks(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, t := range tasks {
		switch t.State {
		case domain.StatePreparing, domain.StateRunning, domain.StateValidating:
			counts[t.Agent]++
		}
	}
	return counts, nil
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	running, err := s.runningPerAgent(ctx)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	limits := s.currentLimits()
	infos := []AgentInfo{}
	if s.agents != nil {
		infos = s.agents.ListAgents(ctx)
	}
	out := make([]agentView, 0, len(infos))
	for _, a := range infos {
		limit := limits.DefaultPerAgent
		if v, ok := limits.PerAgent[a.Name]; ok {
			limit = v
		}
		out = append(out, agentView{
			Name:         a.Name,
			Available:    a.Available,
			Version:      a.Version,
			Path:         a.Path,
			Capabilities: a.Capabilities,
			Running:      running[a.Name],
			Limit:        limit,
			Error:        a.Error,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out, "limits": limits})
}

// limitsPatch is the PATCH /agents/limits body. Nil fields are left unchanged.
type limitsPatch struct {
	Global          *int           `json:"global"`
	DefaultPerAgent *int           `json:"defaultPerAgent"`
	PerAgent        map[string]int `json:"perAgent"`
}

func (s *Server) handlePatchAgentLimits(w http.ResponseWriter, r *http.Request, p *principal) {
	var body limitsPatch
	if !decodeJSON(w, r, &body, maxLimitsBodyBytes) {
		return
	}
	if s.sched == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "scheduler is not configured")
		return
	}
	if (body.Global != nil && *body.Global < 0) || (body.DefaultPerAgent != nil && *body.DefaultPerAgent < 0) {
		writeError(w, http.StatusBadRequest, "bad_request", "limits must be non-negative")
		return
	}
	for name, v := range body.PerAgent {
		if !agentNameRe.MatchString(name) {
			writeError(w, http.StatusBadRequest, "bad_request", "invalid agent name in perAgent")
			return
		}
		if v < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "limits must be non-negative")
			return
		}
	}

	next := s.currentLimits()
	merged := make(map[string]int, len(next.PerAgent)+len(body.PerAgent))
	for k, v := range next.PerAgent {
		merged[k] = v
	}
	for k, v := range body.PerAgent {
		merged[k] = v
	}
	next.PerAgent = merged
	if body.Global != nil {
		next.Global = *body.Global
	}
	if body.DefaultPerAgent != nil {
		next.DefaultPerAgent = *body.DefaultPerAgent
	}

	raw, err := json.Marshal(next)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	ctx := r.Context()
	if err := s.st.SetSetting(ctx, settingSchedulerLimits, string(raw)); err != nil {
		writeDomainError(w, err)
		return
	}
	s.sched.SetLimits(next)
	s.audit(r, &p.User, "agents.limits", "settings", settingSchedulerLimits, next)
	writeJSON(w, http.StatusOK, next)
}
