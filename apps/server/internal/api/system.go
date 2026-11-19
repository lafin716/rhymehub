package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// versionRe is the accepted update target: semver-ish, starting with a digit, no leading '-'.
var versionRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([.+_-][0-9A-Za-z.+_-]*)?$`)

// maxVersionLen bounds update versions and update IDs.
const maxVersionLen = 64

// maxSystemBodyBytes bounds the small JSON bodies of the system endpoints.
const maxSystemBodyBytes = 4 << 10

// supervisorInfo returns the optional status/history view of the supervisor, if configured.
func (s *Server) supervisorInfo() (SupervisorInfo, bool) {
	if s.supervisor == nil {
		return nil, false
	}
	info, ok := s.supervisor.(SupervisorInfo)
	return info, ok
}

// supervisorStatus reports the supervisor as seen by the core. Unconfigured or non-reporting supervisors
// are reported as unreachable.
func (s *Server) supervisorStatus(ctx context.Context) SupervisorStatus {
	info, ok := s.supervisorInfo()
	if !ok {
		return SupervisorStatus{}
	}
	return info.Status(ctx)
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	schema := s.schemaVersion()
	active := 0
	if s.sched != nil {
		active = s.sched.ActiveWorkers()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":       s.version,
		"schemaVersion": schema,
		"uptimeSec":     int(s.now().Sub(s.started) / time.Second),
		"draining":      s.Draining(),
		"home":          s.paths.Root,
		"activeWorkers": active,
		"supervisor":    s.supervisorStatus(ctx),
	})
}

func (s *Server) handleSystemUpdates(w http.ResponseWriter, r *http.Request, _ *principal) {
	history := []json.RawMessage{}
	if info, ok := s.supervisorInfo(); ok {
		recs, err := info.UpdateHistory(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "supervisor is not reachable")
			return
		}
		if recs != nil {
			history = recs
		}
	}
	// available is empty in v0.1: the core does not yet check a release feed for newer versions.
	writeJSON(w, http.StatusOK, map[string]any{"available": []any{}, "history": history})
}

type systemUpdateBody struct {
	Version string `json:"version"`
}

func (s *Server) handleSystemUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var body systemUpdateBody
	if !decodeJSON(w, r, &body, maxSystemBodyBytes) {
		return
	}
	if len(body.Version) > maxVersionLen || !versionRe.MatchString(body.Version) {
		writeError(w, http.StatusBadRequest, "bad_request", "version must be semver-like, for example 0.3.0")
		return
	}
	if s.supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "supervisor is not configured")
		return
	}
	if _, ok := s.supervisorInfo(); ok {
		st := s.supervisorStatus(r.Context())
		if !st.Reachable {
			writeError(w, http.StatusServiceUnavailable, "internal", "supervisor is not reachable")
			return
		}
		if st.UpdateInProgress {
			writeError(w, http.StatusConflict, "conflict", "an update or rollback is already in progress")
			return
		}
	}
	s.audit(r, &p.User, "system.update", "system", body.Version, map[string]any{"version": body.Version})
	// The supervisor blocks until the update finishes, so the forward runs detached from the request.
	sup := s.supervisor
	go func() {
		if err := sup.Update(context.Background(), body.Version); err != nil {
			s.log.Error("supervisor update failed", "version", body.Version, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

type systemRollbackBody struct {
	UpdateID string `json:"updateId"`
}

func (s *Server) handleSystemRollback(w http.ResponseWriter, r *http.Request, p *principal) {
	var body systemRollbackBody
	if !decodeJSON(w, r, &body, maxSystemBodyBytes) {
		return
	}
	body.UpdateID = strings.TrimSpace(body.UpdateID)
	if body.UpdateID == "" || len(body.UpdateID) > maxVersionLen {
		writeError(w, http.StatusBadRequest, "bad_request", "updateId is required")
		return
	}
	if s.supervisor == nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "supervisor is not configured")
		return
	}
	if info, ok := s.supervisorInfo(); ok {
		st := s.supervisorStatus(r.Context())
		if !st.Reachable {
			writeError(w, http.StatusServiceUnavailable, "internal", "supervisor is not reachable")
			return
		}
		if st.UpdateInProgress {
			writeError(w, http.StatusConflict, "conflict", "an update or rollback is already in progress")
			return
		}
		recs, err := info.UpdateHistory(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "internal", "supervisor is not reachable")
			return
		}
		if !hasUpdateRecord(recs, body.UpdateID) {
			writeError(w, http.StatusNotFound, "not_found", "no update with that id in history")
			return
		}
	}
	s.audit(r, &p.User, "system.rollback", "system", body.UpdateID, map[string]any{"updateId": body.UpdateID})
	sup := s.supervisor
	go func() {
		if err := sup.Rollback(context.Background(), body.UpdateID); err != nil {
			s.log.Error("supervisor rollback failed", "updateId", body.UpdateID, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// hasUpdateRecord reports whether one of the raw supervisor update records has the given id.
func hasUpdateRecord(recs []json.RawMessage, id string) bool {
	for _, raw := range recs {
		var rec struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &rec); err == nil && rec.ID == id {
			return true
		}
	}
	return false
}

// supervisorTokenOK checks `Authorization: Bearer <supToken>` in constant time. An empty configured token
// never matches.
func (s *Server) supervisorTokenOK(r *http.Request) bool {
	if s.supToken == "" {
		return false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.supToken)) == 1
}

type drainBody struct {
	Drain *bool `json:"drain"`
}

// handleSystemDrain is called by the supervisor (not a user), so it is not wrapped by need().
func (s *Server) handleSystemDrain(w http.ResponseWriter, r *http.Request) {
	if !s.supervisorTokenOK(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "supervisor token required")
		return
	}
	var body drainBody
	if !decodeJSON(w, r, &body, maxSystemBodyBytes) {
		return
	}
	if body.Drain == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "drain is required")
		return
	}
	s.SetDraining(*body.Drain)
	if s.sched != nil {
		s.sched.Drain(*body.Drain)
	}
	active := 0
	if s.sched != nil {
		active = s.sched.ActiveWorkers()
	}
	writeJSON(w, http.StatusOK, map[string]any{"draining": s.Draining(), "activeWorkers": active})
}
