package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
)

const maxDiagnosticPromptBytes = 8 << 10

var requestStatuses = []domain.RequestStatus{
	domain.RequestNew, domain.RequestTriaged, domain.RequestInProgress, domain.RequestWaitingInput,
	domain.RequestReview, domain.RequestDone, domain.RequestRejected, domain.RequestFailed,
}

// requestView is the Request shape. Task is included on the single-item endpoint only.
type requestView struct {
	domain.Request
	Task *taskSummary `json:"task,omitempty"`
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request, _ *principal) {
	q := r.URL.Query()
	limit, ok := parseLimit(w, q.Get("limit"), defaultLimit)
	if !ok {
		return
	}
	status := domain.RequestStatus(q.Get("status"))
	if status != "" && !slices.Contains(requestStatuses, status) {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown status")
		return
	}
	source := domain.RequestSource(q.Get("source"))
	if source != "" && !source.Valid() {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown source")
		return
	}
	typ := domain.RequestType(q.Get("type"))
	if typ != "" && !typ.Valid() {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown type")
		return
	}
	ctx := r.Context()
	projectIDs := []string{}
	if pid := q.Get("projectId"); pid != "" {
		projectIDs = append(projectIDs, pid)
	} else {
		// The store filters by project only, so the cross-project list is assembled here.
		projs, err := s.st.ListProjects(ctx, true)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		for _, p := range projs {
			projectIDs = append(projectIDs, p.ID)
		}
	}
	out := []domain.Request{}
	for _, pid := range projectIDs {
		reqs, err := s.st.ListRequests(ctx, pid, status, 0)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		for _, rq := range reqs {
			if source != "" && rq.Source != source {
				continue
			}
			if typ != "" && rq.Type != typ {
				continue
			}
			out = append(out, rq)
		}
	}
	slices.SortStableFunc(out, func(a, b domain.Request) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

type createRequestBody struct {
	ProjectID   string             `json:"projectId"`
	Type        domain.RequestType `json:"type"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Priority    *int               `json:"priority"`
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request, p *principal) {
	var body createRequestBody
	if !decodeJSON(w, r, &body, maxPromptBytes) {
		return
	}
	if body.ProjectID == "" || !body.Type.Valid() || !validText(body.Description, maxTextBytes) {
		writeError(w, http.StatusBadRequest, "bad_request", "projectId, type and description are required")
		return
	}
	priority, ok := resolvePriority(body.Priority)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_request", "priority must be between 0 and 4")
		return
	}
	ctx := r.Context()
	proj, err := s.st.GetProject(ctx, body.ProjectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if proj.Archived {
		writeError(w, http.StatusBadRequest, "bad_request", "project is archived")
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = firstLine(body.Description, 120)
	} else {
		title = firstLine(title, 200)
	}
	// Dashboard sessions and CLI/API tokens are the only callers of this route; SDK keys never reach it.
	source := domain.SourceCLI
	if p.Cookie {
		source = domain.SourceDashboard
	}
	rq, err := s.st.CreateRequest(ctx, domain.Request{
		ProjectID: proj.ID, Source: source, Type: body.Type, Title: title,
		Description: body.Description, Priority: priority, ReporterUserID: p.User.ID,
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "request.create", "request", rq.ID, map[string]any{"projectId": proj.ID, "type": rq.Type})
	writeJSON(w, http.StatusCreated, requestView{Request: rq})
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	rq, err := s.st.GetRequest(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	v := requestView{Request: rq}
	if rq.TaskID != "" {
		if t, err := s.st.GetTask(ctx, rq.TaskID); err == nil {
			ts := summaryOf(t)
			v.Task = &ts
		}
	}
	writeJSON(w, http.StatusOK, v)
}

type patchRequestBody struct {
	Priority *int    `json:"priority"`
	Status   *string `json:"status"`
}

// handlePatchRequest updates triage fields. priority (0 low .. 4 urgent, DATA_MODEL) and status (triaged or
// rejected) are both optional but at least one is required. Everything is validated before any write, so a
// rejected status never leaves a changed priority behind. Status moves: new -> triaged, or new/triaged -> rejected.
func (s *Server) handlePatchRequest(w http.ResponseWriter, r *http.Request, p *principal) {
	var body patchRequestBody
	if !decodeJSON(w, r, &body, maxBodyBytes) {
		return
	}
	if body.Priority == nil && body.Status == nil {
		writeError(w, http.StatusBadRequest, "bad_request", "priority or status is required")
		return
	}
	if body.Priority != nil && (*body.Priority < 0 || *body.Priority > 4) {
		writeError(w, http.StatusBadRequest, "bad_request", "priority must be between 0 and 4")
		return
	}
	var next domain.RequestStatus
	if body.Status != nil {
		next = domain.RequestStatus(*body.Status)
		if next != domain.RequestTriaged && next != domain.RequestRejected {
			writeError(w, http.StatusBadRequest, "bad_request", "status may only be set to triaged or rejected")
			return
		}
	}
	id := r.PathValue("id")
	ctx := r.Context()
	rq, err := s.st.GetRequest(ctx, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if body.Status != nil {
		allowed := rq.Status == domain.RequestNew || (next == domain.RequestRejected && rq.Status == domain.RequestTriaged)
		if !allowed {
			writeError(w, http.StatusConflict, "conflict", "request is already "+string(rq.Status))
			return
		}
	}
	if body.Priority != nil {
		if err := s.st.SetRequestPriority(ctx, id, *body.Priority); err != nil {
			writeDomainError(w, err)
			return
		}
		s.audit(r, &p.User, "request.priority", "request", id, map[string]any{"priority": *body.Priority})
	}
	if body.Status != nil {
		if err := s.st.SetRequestStatus(ctx, id, next, ""); err != nil {
			writeDomainError(w, err)
			return
		}
		s.audit(r, &p.User, "request.triage", "request", id, map[string]any{"status": next})
	}
	updated, err := s.st.GetRequest(ctx, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, requestView{Request: updated})
}

type taskFromRequestBody struct {
	Agent string `json:"agent"`
	Model string `json:"model"`
}

// handleCreateTaskFromRequest creates a task from an inbox item and links it to the request.
func (s *Server) handleCreateTaskFromRequest(w http.ResponseWriter, r *http.Request, p *principal) {
	var body taskFromRequestBody
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	if s.Draining() {
		writeError(w, http.StatusConflict, "conflict", "server is draining; new tasks are refused")
		return
	}
	ctx := r.Context()
	rq, err := s.st.GetRequest(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if rq.Status == domain.RequestRejected {
		writeError(w, http.StatusConflict, "conflict", "request is rejected")
		return
	}
	if rq.TaskID != "" {
		if prev, err := s.st.GetTask(ctx, rq.TaskID); err == nil && domain.IsActive(prev.State) {
			writeError(w, http.StatusConflict, "conflict", "request already has an active task")
			return
		}
	}
	proj, err := s.st.GetProject(ctx, rq.ProjectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	t, err := s.enqueueTask(ctx, domain.Task{
		RequestID: rq.ID, Title: firstLine(rq.Title, 120), Prompt: buildRequestPrompt(rq),
		Agent: body.Agent, Model: body.Model, Priority: rq.Priority, CreatedBy: p.User.ID,
	}, proj)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if err := s.st.SetRequestStatus(ctx, rq.ID, domain.RequestStatusFor(t.State), t.ID); err != nil {
		s.log.Error("link request to task", "request", rq.ID, "task", t.ID, "err", err)
	}
	s.audit(r, &p.User, "request.task", "request", rq.ID, map[string]any{"taskId": t.ID})
	writeJSON(w, http.StatusCreated, detailOf(t))
}

// buildRequestPrompt turns an inbox item into the agent prompt. Diagnostics are included as text and are capped.
func buildRequestPrompt(rq domain.Request) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Request %s (%s): %s\n\n%s", rq.ID, rq.Type, rq.Title, strings.TrimSpace(rq.Description))
	for _, d := range rq.Diagnostics {
		if d.Content == "" {
			continue
		}
		content := d.Content
		if len(content) > maxDiagnosticPromptBytes {
			content = content[:maxDiagnosticPromptBytes] + "\n[truncated]"
		}
		fmt.Fprintf(&b, "\n\n[%s: %s]\n%s", d.Kind, d.Name, content)
	}
	out := b.String()
	if len(out) > maxPromptBytes {
		out = out[:maxPromptBytes]
	}
	return out
}

// requestStatusFor reads a request's current status. Used by tests and the SSE stream.
func (s *Server) requestStatusFor(ctx context.Context, id string) (domain.RequestStatus, error) {
	rq, err := s.st.GetRequest(ctx, id)
	if err != nil {
		return "", err
	}
	return rq.Status, nil
}

// defaultPriority is the DATA_MODEL default (2 = normal); priorities run 0 low .. 4 urgent.
const defaultPriority = 2

// resolvePriority applies the default for an omitted priority and validates the 0..4 range.
func resolvePriority(p *int) (int, bool) {
	if p == nil {
		return defaultPriority, true
	}
	if *p < 0 || *p > 4 {
		return 0, false
	}
	return *p, true
}
