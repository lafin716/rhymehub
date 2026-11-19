package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
	"github.com/lafin716/rhymehub/internal/workspace"
)

const (
	maxPromptBytes = 64 << 10
	maxTextBytes   = 16 << 10
	maxDiffBytes   = 1 << 20
	defaultLimit   = 100
	maxLimit       = 500
	defaultAgent   = "claude"
	releaseTimeout = 30 * time.Minute
	diffTimeout    = 30 * time.Second
	cancelTimeout  = 10 * time.Second
)

var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// forbiddenToolFragments lists tool-permission fragments that a permission decision can never grant,
// whatever the project policy says. Placeholder list: it must be reconciled with docs/SECURITY.md.
var forbiddenToolFragments = []string{"sudo", "rm -rf", "mkfs", "shutdown", "launchctl"}

func isForbiddenTool(name string) bool {
	n := strings.ToLower(name)
	for _, f := range forbiddenToolFragments {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}

// taskSummary is the TaskSummary shape from docs/API_SPEC.md.
type taskSummary struct {
	ID              string           `json:"id"`
	ProjectID       string           `json:"projectId"`
	RequestID       string           `json:"requestId"`
	Title           string           `json:"title"`
	Agent           string           `json:"agent"`
	Model           string           `json:"model"`
	State           domain.TaskState `json:"state"`
	Priority        int              `json:"priority"`
	Branch          string           `json:"branch"`
	TestStatus      string           `json:"testStatus"`
	DiffStat        string           `json:"diffStat"`
	ResultSummary   string           `json:"resultSummary"`
	PendingQuestion string           `json:"pendingQuestion"`
	PendingApproval json.RawMessage  `json:"pendingApproval"`
	FailureReason   string           `json:"failureReason"`
	Error           string           `json:"error"`
	CostUSD         float64          `json:"costUsd"`
	Turns           int              `json:"turns"`
	Attempt         int              `json:"attempt"`
	CreatedAt       time.Time        `json:"createdAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
	StartedAt       *time.Time       `json:"startedAt"`
	FinishedAt      *time.Time       `json:"finishedAt"`
}

// taskDetail is the Task shape: TaskSummary plus the internal fields a maintainer or CLI needs.
type taskDetail struct {
	taskSummary
	Prompt       string   `json:"prompt"`
	WorktreePath string   `json:"worktreePath"`
	BaseCommit   string   `json:"baseCommit"`
	HeadCommit   string   `json:"headCommit"`
	SessionID    string   `json:"sessionId"`
	TestOutput   string   `json:"testOutput"`
	AllowedTools []string `json:"allowedTools"`
	ApprovedBy   string   `json:"approvedBy"`
	ReleaseID    string   `json:"releaseId"`
}

func summaryOf(t domain.Task) taskSummary {
	var pending json.RawMessage
	if len(t.PendingApproval) > 0 {
		pending = t.PendingApproval
	}
	return taskSummary{
		ID: t.ID, ProjectID: t.ProjectID, RequestID: t.RequestID, Title: t.Title, Agent: t.Agent, Model: t.Model,
		State: t.State, Priority: t.Priority, Branch: t.Branch, TestStatus: t.TestStatus, DiffStat: t.DiffStat,
		ResultSummary: t.ResultSummary, PendingQuestion: t.PendingQuestion, PendingApproval: pending,
		FailureReason: t.FailureReason, Error: t.Error, CostUSD: t.CostUSD, Turns: t.Turns, Attempt: t.Attempt,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, StartedAt: t.StartedAt, FinishedAt: t.FinishedAt,
	}
}

func detailOf(t domain.Task) taskDetail {
	tools := t.AllowedTools
	if tools == nil {
		tools = []string{}
	}
	return taskDetail{
		taskSummary: summaryOf(t), Prompt: t.Prompt, WorktreePath: t.WorktreePath, BaseCommit: t.BaseCommit,
		HeadCommit: t.HeadCommit, SessionID: t.SessionID, TestOutput: t.TestOutput, AllowedTools: tools,
		ApprovedBy: t.ApprovedBy, ReleaseID: t.ReleaseID,
	}
}

// taskEventView is the TaskEvent shape. Seq is the global event id, per docs/API_SPEC.md.
type taskEventView struct {
	ID        int64            `json:"id"`
	TaskID    string           `json:"taskId"`
	Seq       int64            `json:"seq"`
	Type      domain.EventType `json:"type"`
	Payload   json.RawMessage  `json:"payload"`
	CreatedAt time.Time        `json:"createdAt"`
}

func eventView(e domain.TaskEvent) taskEventView {
	return taskEventView{ID: e.ID, TaskID: e.TaskID, Seq: e.ID, Type: e.Type, Payload: e.Payload, CreatedAt: e.CreatedAt}
}

// transition moves a task and keeps the linked request status in step with the new state.
func (s *Server) transition(ctx context.Context, id string, from []domain.TaskState, to domain.TaskState, ev *store.EventInput) (domain.Task, error) {
	t, err := s.st.TransitionTask(ctx, id, from, to, ev)
	if err != nil {
		return domain.Task{}, err
	}
	if t.RequestID != "" {
		if err := s.st.SetRequestStatus(ctx, t.RequestID, domain.RequestStatusFor(to), ""); err != nil {
			s.log.Error("sync request status", "task", id, "err", err)
		}
	}
	return t, nil
}

// enqueueTask creates a task in CREATED and moves it to QUEUED. It applies project defaults.
func (s *Server) enqueueTask(ctx context.Context, t domain.Task, proj domain.Project) (domain.Task, error) {
	if proj.Archived {
		return domain.Task{}, errors.Join(domain.ErrInvalid, errors.New("project is archived"))
	}
	if t.Agent == "" {
		t.Agent = proj.DefaultAgent
	}
	if t.Agent == "" {
		t.Agent = defaultAgent
	}
	if t.Model == "" {
		t.Model = proj.DefaultModel
	}
	if !agentNameRe.MatchString(t.Agent) {
		return domain.Task{}, errors.Join(domain.ErrInvalid, errors.New("invalid agent name"))
	}
	if len(t.Model) > 64 {
		return domain.Task{}, errors.Join(domain.ErrInvalid, errors.New("model name too long"))
	}
	t.ProjectID = proj.ID
	t.AllowedTools = slices.Clone(proj.AllowedTools)
	created, err := s.st.CreateTask(ctx, t)
	if err != nil {
		return domain.Task{}, err
	}
	return s.transition(ctx, created.ID, []domain.TaskState{domain.StateCreated}, domain.StateQueued, &store.EventInput{Reason: "queued"})
}

// loadTask resolves the {id} path value. It writes the error response and returns false on failure.
func (s *Server) loadTask(w http.ResponseWriter, r *http.Request) (domain.Task, bool) {
	t, err := s.st.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return domain.Task{}, false
	}
	return t, true
}

func validText(s string, maxBytes int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= maxBytes
}

// firstLine returns the first non-empty line of s, trimmed and cut to n runes.
func firstLine(s string, n int) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	r := []rune(line)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// parseLimit reads a positive ?limit value, capped at maxLimit. It writes the error response on failure.
func parseLimit(w http.ResponseWriter, raw string, def int) (int, bool) {
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "bad_request", "limit must be a positive integer")
		return 0, false
	}
	return min(n, maxLimit), true
}

// decodeOptionalJSON is decodeJSON for bodies that may be empty.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// --- listing and creation ---------------------------------------------------------------------

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request, _ *principal) {
	q := r.URL.Query()
	limit, ok := parseLimit(w, q.Get("limit"), defaultLimit)
	if !ok {
		return
	}
	state := domain.TaskState(q.Get("state"))
	if state != "" && !state.Valid() {
		writeError(w, http.StatusBadRequest, "bad_request", "unknown state")
		return
	}
	activeOnly := q.Get("active") == "1"
	fetch := limit
	if state != "" || activeOnly {
		fetch = 0 // filters are applied in memory, so read everything for the project
	}
	tasks, err := s.st.ListTasks(r.Context(), q.Get("projectId"), fetch)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := []taskSummary{}
	for _, t := range tasks {
		if state != "" && t.State != state {
			continue
		}
		if activeOnly && !domain.IsActive(t.State) {
			continue
		}
		out = append(out, summaryOf(t))
		if len(out) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
}

type createTaskBody struct {
	ProjectID string `json:"projectId"`
	Prompt    string `json:"prompt"`
	Title     string `json:"title"`
	Agent     string `json:"agent"`
	Model     string `json:"model"`
	Priority  int    `json:"priority"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request, p *principal) {
	var body createTaskBody
	if !decodeJSON(w, r, &body, maxPromptBytes) {
		return
	}
	if body.ProjectID == "" || !validText(body.Prompt, maxPromptBytes) {
		writeError(w, http.StatusBadRequest, "bad_request", "projectId and a non-empty prompt are required")
		return
	}
	if s.Draining() {
		writeError(w, http.StatusConflict, "conflict", "server is draining; new tasks are refused")
		return
	}
	ctx := r.Context()
	proj, err := s.st.GetProject(ctx, body.ProjectID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	title := strings.TrimSpace(body.Title)
	if title == "" {
		title = firstLine(body.Prompt, 120)
	}
	t, err := s.enqueueTask(ctx, domain.Task{
		Title: title, Prompt: body.Prompt, Agent: body.Agent, Model: body.Model, Priority: body.Priority,
		CreatedBy: p.User.ID,
	}, proj)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "task.create", "task", t.ID, map[string]any{"projectId": proj.ID, "agent": t.Agent})
	writeJSON(w, http.StatusCreated, detailOf(t))
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request, _ *principal) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, detailOf(t))
}

func (s *Server) handleTaskEvents(w http.ResponseWriter, r *http.Request, _ *principal) {
	id := r.PathValue("id")
	ctx := r.Context()
	if _, err := s.st.GetTask(ctx, id); err != nil {
		writeDomainError(w, err)
		return
	}
	q := r.URL.Query()
	raw := q.Get("after")
	if raw == "" {
		raw = q.Get("afterSeq")
	}
	var after int64
	if raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "after must be a non-negative integer")
			return
		}
		after = n
	}
	limit, ok := parseLimit(w, q.Get("limit"), maxLimit)
	if !ok {
		return
	}
	evs, err := s.st.ListEventsSince(ctx, id, after, limit)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	out := make([]taskEventView, 0, len(evs))
	for _, e := range evs {
		out = append(out, eventView(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// --- follow-ups, answers, permissions ---------------------------------------------------------

type textBody struct {
	Text string `json:"text"`
}

func (s *Server) handleTaskMessage(w http.ResponseWriter, r *http.Request, p *principal) {
	var body textBody
	if !decodeJSON(w, r, &body, maxTextBytes) {
		return
	}
	if !validText(body.Text, maxTextBytes) {
		writeError(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	_, err := s.transition(ctx, id, []domain.TaskState{domain.StateReviewReady, domain.StateFailed, domain.StateWaitingInput},
		domain.StateQueued, &store.EventInput{Reason: "follow-up message"})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	t, err := s.st.MutateTask(ctx, id, func(t *domain.Task) error {
		t.NextInput = body.Text
		return nil
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.st.AppendTaskEvent(ctx, id, store.NewEvent{
		Type: domain.EventUserMessage, Payload: rawJSON(map[string]any{"text": body.Text, "author": p.User.Username}),
	}); err != nil {
		s.log.Error("append user_message", "task", id, "err", err)
	}
	s.audit(r, &p.User, "task.message", "task", id, nil)
	writeJSON(w, http.StatusOK, detailOf(t))
}

func (s *Server) handleTaskAnswer(w http.ResponseWriter, r *http.Request, p *principal) {
	var body textBody
	if !decodeJSON(w, r, &body, maxTextBytes) {
		return
	}
	if !validText(body.Text, maxTextBytes) {
		writeError(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	_, err := s.transition(ctx, id, []domain.TaskState{domain.StateWaitingInput}, domain.StateQueued,
		&store.EventInput{Reason: "question answered"})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	t, err := s.st.MutateTask(ctx, id, func(t *domain.Task) error {
		t.NextInput = body.Text
		t.PendingQuestion = ""
		return nil
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.st.AppendTaskEvent(ctx, id, store.NewEvent{
		Type: domain.EventAnswer, Payload: rawJSON(map[string]any{"text": body.Text, "author": p.User.Username}),
	}); err != nil {
		s.log.Error("append answer", "task", id, "err", err)
	}
	s.audit(r, &p.User, "task.answer", "task", id, nil)
	writeJSON(w, http.StatusOK, detailOf(t))
}

type pendingTool struct {
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

type permissionBody struct {
	Allow bool `json:"allow"`
}

func (s *Server) handlePermission(w http.ResponseWriter, r *http.Request, p *principal) {
	var body permissionBody
	if !decodeJSON(w, r, &body, 4<<10) {
		return
	}
	id := r.PathValue("id")
	ctx := r.Context()
	cur, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	var pending []pendingTool
	if len(cur.PendingApproval) > 0 {
		if err := json.Unmarshal(cur.PendingApproval, &pending); err != nil {
			s.log.Error("decode pending approval", "task", id, "err", err)
			pending = nil
		}
	}
	var granted, refused []string
	if body.Allow {
		for _, pt := range pending {
			switch {
			case pt.Tool == "":
			case isForbiddenTool(pt.Tool):
				refused = append(refused, pt.Tool)
			case !slices.Contains(granted, pt.Tool):
				granted = append(granted, pt.Tool)
			}
		}
	}
	_, err := s.transition(ctx, id, []domain.TaskState{domain.StateWaitingApproval}, domain.StateQueued,
		&store.EventInput{Reason: "permission decision"})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	t, err := s.st.MutateTask(ctx, id, func(t *domain.Task) error {
		for _, g := range granted {
			if !slices.Contains(t.AllowedTools, g) {
				t.AllowedTools = append(t.AllowedTools, g)
			}
		}
		t.PendingApproval = nil
		return nil
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if granted == nil {
		granted = []string{}
	}
	if _, err := s.st.AppendTaskEvent(ctx, id, store.NewEvent{
		Type: domain.EventPermissionDecision,
		Payload: rawJSON(map[string]any{"allow": body.Allow, "tools": granted, "refused": refused,
			"author": p.User.Username}),
	}); err != nil {
		s.log.Error("append permission_decision", "task", id, "err", err)
	}
	s.audit(r, &p.User, "task.permission", "task", id, map[string]any{"allow": body.Allow, "tools": granted, "refused": refused})
	writeJSON(w, http.StatusOK, detailOf(t))
}

// --- cancel, retry, diff ----------------------------------------------------------------------

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request, p *principal) {
	cur, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	if err := domain.CheckTransition(cur.State, domain.StateCancelled); err != nil {
		writeDomainError(w, err)
		return
	}
	wasRunning := domain.IsRunning(cur.State)
	ctx := r.Context()
	t, err := s.transition(ctx, cur.ID, []domain.TaskState{cur.State}, domain.StateCancelled,
		&store.EventInput{Reason: "cancelled by " + p.User.Username})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if wasRunning && s.canceller != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
		if err := s.canceller.CancelTask(cctx, cur.ID); err != nil {
			s.log.Error("stop worker", "task", cur.ID, "err", err)
		}
		cancel()
	}
	s.audit(r, &p.User, "task.cancel", "task", cur.ID, map[string]any{"from": cur.State})
	writeJSON(w, http.StatusOK, detailOf(t))
}

func (s *Server) handleTaskRetry(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	ctx := r.Context()
	_, err := s.transition(ctx, id, []domain.TaskState{domain.StateFailed}, domain.StateQueued,
		&store.EventInput{Reason: "retry requested by " + p.User.Username})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	t, err := s.st.MutateTask(ctx, id, func(t *domain.Task) error {
		t.Attempt = 0
		t.Error = ""
		t.FailureReason = ""
		return nil
	})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "task.retry", "task", id, nil)
	writeJSON(w, http.StatusOK, detailOf(t))
}

type diffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// parseDiffFiles summarises a unified diff: one entry per "diff --git" header, with +/- counts taken from
// hunk bodies only (so "---"/"+++" file headers are not counted).
func parseDiffFiles(patch string) []diffFile {
	files := []diffFile{}
	idx := -1
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, diffFile{Status: "modified"})
			idx = len(files) - 1
			inHunk = false
			if i := strings.LastIndex(line, " b/"); i >= 0 {
				files[idx].Path = line[i+3:]
			}
		case idx < 0:
		case strings.HasPrefix(line, "new file mode"):
			files[idx].Status = "added"
		case strings.HasPrefix(line, "deleted file mode"):
			files[idx].Status = "deleted"
		case strings.HasPrefix(line, "rename from"):
			files[idx].Status = "renamed"
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk:
		case strings.HasPrefix(line, "+"):
			files[idx].Additions++
		case strings.HasPrefix(line, "-"):
			files[idx].Deletions++
		}
	}
	return files
}

func (s *Server) handleTaskDiff(w http.ResponseWriter, r *http.Request, _ *principal) {
	t, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	if t.WorktreePath == "" || t.BaseCommit == "" {
		writeJSON(w, http.StatusOK, map[string]any{"base": t.BaseCommit, "head": t.HeadCommit, "stat": "",
			"files": []diffFile{}, "patch": "", "truncated": false})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), diffTimeout)
	defer cancel()
	res, err := workspace.Diff(ctx, t.WorktreePath, t.BaseCommit)
	if err != nil {
		s.log.Error("diff worktree", "task", t.ID, "err", err)
		writeError(w, http.StatusConflict, "conflict", "worktree is not available")
		return
	}
	patch := res.Patch
	truncated := false
	if len(patch) > maxDiffBytes {
		patch = patch[:maxDiffBytes]
		truncated = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"base": t.BaseCommit, "head": t.HeadCommit, "stat": res.Stat,
		"files": parseDiffFiles(res.Patch), "patch": patch, "truncated": truncated})
}

// --- review: approve, reject ------------------------------------------------------------------

type approveBody struct {
	Release *bool `json:"release"`
}

// handleApprove moves REVIEW_READY -> APPROVED -> MERGING and starts the release asynchronously.
// Refused with 409 when the test status is failed.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request, p *principal) {
	var body approveBody
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	release := body.Release == nil || *body.Release
	id := r.PathValue("id")
	ctx := r.Context()
	cur, ok := s.loadTask(w, r)
	if !ok {
		return
	}
	if cur.TestStatus == "failed" {
		writeError(w, http.StatusConflict, "conflict", "tests failed; approval refused")
		return
	}
	_, err := s.transition(ctx, id, []domain.TaskState{domain.StateReviewReady}, domain.StateApproved,
		&store.EventInput{Reason: "approved by " + p.User.Username, Detail: rawJSON(map[string]any{"release": release})})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if _, err := s.st.MutateTask(ctx, id, func(t *domain.Task) error {
		t.ApprovedBy = p.User.ID
		return nil
	}); err != nil {
		writeDomainError(w, err)
		return
	}
	t, err := s.transition(ctx, id, []domain.TaskState{domain.StateApproved}, domain.StateMerging,
		&store.EventInput{Reason: "merge started"})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "task.approve", "task", id, map[string]any{"release": release})
	go s.runRelease(id, release)
	writeJSON(w, http.StatusOK, detailOf(t))
}

// runRelease calls the releaser outside the request. If it fails while the task is still MERGING, the task
// moves to FAILED with a generic reason; the raw error is only logged.
func (s *Server) runRelease(taskID string, release bool) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	err := s.releaser.ReleaseTask(ctx, taskID, release)
	if err == nil {
		return
	}
	s.log.Error("release failed", "task", taskID, "err", err)
	reason := "release failed"
	if errors.Is(err, ErrNotConfigured) {
		reason = "release is not configured on this server"
	}
	if _, err := s.transition(ctx, taskID, []domain.TaskState{domain.StateMerging}, domain.StateFailed,
		&store.EventInput{Reason: reason}); err != nil {
		// The releaser may already have moved the task on; nothing more to do.
		return
	}
	if _, err := s.st.MutateTask(ctx, taskID, func(t *domain.Task) error {
		t.FailureReason = "release_failed"
		t.Error = reason
		return nil
	}); err != nil {
		s.log.Error("record release failure", "task", taskID, "err", err)
	}
}

type rejectBody struct {
	Reason string `json:"reason"`
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request, p *principal) {
	var body rejectBody
	if !decodeJSON(w, r, &body, maxTextBytes) {
		return
	}
	if !validText(body.Reason, 2<<10) {
		writeError(w, http.StatusBadRequest, "bad_request", "reason is required")
		return
	}
	id := r.PathValue("id")
	t, err := s.transition(r.Context(), id, []domain.TaskState{domain.StateReviewReady}, domain.StateCancelled,
		&store.EventInput{Reason: "rejected by " + p.User.Username, Detail: rawJSON(map[string]any{"reason": body.Reason})})
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "task.reject", "task", id, map[string]any{"reason": body.Reason})
	writeJSON(w, http.StatusOK, detailOf(t))
}
