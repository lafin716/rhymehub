package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/workspace"
)

const (
	maxProjectNameLen  = 100
	maxProjectTypeLen  = 50
	maxCmdBytes        = 4 << 10
	maxShortTextBytes  = 200
	maxAllowedTools    = 50
	maxToolNameLen     = 100
	maxMaxConcurrent   = 32
	maxTimeoutMinutes  = 1440
	maxDiscoverDepth   = 2
	maxDiscoverResults = 200
)

var (
	projectIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	branchNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

// releaseStrategies are the values accepted for projects.release_strategy (docs/RELEASE_AND_UPDATE.md).
var releaseStrategies = map[string]bool{"none": true, "tag": true, "command": true, "rhymehub-self": true}

// projectStats are the per-project counters shown on the dashboard.
type projectStats struct {
	OpenRequests int `json:"openRequests"`
	ActiveTasks  int `json:"activeTasks"`
	ReviewReady  int `json:"reviewReady"`
	Failed       int `json:"failed"`
}

// projectView is the Project shape from docs/API_SPEC.md. Git is set only on GET /projects/{id}; the list
// endpoint leaves it out so that it does not run git for every project.
type projectView struct {
	domain.Project
	Stats projectStats `json:"stats"`
	Git   *projectGit  `json:"git,omitempty"`
}

// badRequest wraps domain.ErrInvalid so writeDomainError answers 400.
func badRequest(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalid, fmt.Sprintf(format, args...))
}

// asInvalid maps workspace validation errors onto domain.ErrInvalid. Other errors pass through unchanged.
func asInvalid(err error) error {
	if errors.Is(err, workspace.ErrInvalid) {
		return badRequest("%s", err.Error())
	}
	return err
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
	pg := readGitState(ctx, s.projectDir(p))
	writeJSON(w, http.StatusOK, projectView{Project: p, Stats: stats, Git: &pg})
}

// ---- Create ----

type createProjectIn struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	RepoPath             string   `json:"repoPath"`
	GitURL               string   `json:"gitUrl"`
	DefaultBranch        string   `json:"defaultBranch"`
	Type                 string   `json:"type"`
	Stack                string   `json:"stack"`
	BuildCmd             string   `json:"buildCmd"`
	TestCmd              string   `json:"testCmd"`
	ReleaseStrategy      string   `json:"releaseStrategy"`
	ReleaseCmd           string   `json:"releaseCmd"`
	DefaultAgent         string   `json:"defaultAgent"`
	DefaultModel         string   `json:"defaultModel"`
	MaxConcurrent        int      `json:"maxConcurrent"`
	TimeoutMinutes       int      `json:"timeoutMinutes"`
	AutoTaskFromRequests bool     `json:"autoTaskFromRequests"`
	AutoApprove          bool     `json:"autoApprove"`
	AutoRelease          bool     `json:"autoRelease"`
	AllowedTools         []string `json:"allowedTools"`
}

// touchesPolicy reports whether the create body sets any policy field, which requires the maintainer role.
func (in createProjectIn) touchesPolicy() bool {
	return in.BuildCmd != "" || in.TestCmd != "" || in.ReleaseStrategy != "" || in.ReleaseCmd != "" ||
		in.AutoApprove || in.AutoRelease || len(in.AllowedTools) > 0
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request, p *principal) {
	var in createProjectIn
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	if in.touchesPolicy() && !p.User.Role.AtLeast(domain.RoleMaintainer) {
		writeError(w, http.StatusForbidden, "forbidden", "policy fields require role "+string(domain.RoleMaintainer))
		return
	}
	name := strings.TrimSpace(in.Name)
	if err := validateProjectName(name); err != nil {
		writeDomainError(w, err)
		return
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		gen, err := newProjectID()
		if err != nil {
			writeDomainError(w, err)
			return
		}
		id = gen
	} else if !projectIDRe.MatchString(id) {
		writeDomainError(w, badRequest("id must be 1-64 lowercase letters, digits, '_' or '-'"))
		return
	}
	proj, err := buildNewProject(id, name, in)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if (in.RepoPath == "") == (in.GitURL == "") {
		writeDomainError(w, badRequest("exactly one of repoPath or gitUrl is required"))
		return
	}

	ctx := r.Context()
	source := "repoPath"
	var dir string
	cloned := false
	if in.RepoPath != "" {
		if !filepath.IsAbs(in.RepoPath) {
			writeDomainError(w, badRequest("repoPath must be absolute"))
			return
		}
		dir = filepath.Clean(in.RepoPath)
		if err := workspace.ValidateRepo(ctx, dir); err != nil {
			writeDomainError(w, badRequest("repoPath is not a git repository: %v", err))
			return
		}
		proj.RepoPath = dir
	} else {
		if err := workspace.ValidateCloneURL(in.GitURL); err != nil {
			writeDomainError(w, badRequest("invalid gitUrl: %v", err))
			return
		}
		if s.paths.Root == "" {
			writeError(w, http.StatusInternalServerError, "internal", "data directory is not configured")
			return
		}
		source = "gitUrl"
		dir = filepath.Join(s.reposDir(), id)
		if err := os.MkdirAll(s.reposDir(), 0o700); err != nil {
			writeDomainError(w, fmt.Errorf("create repos dir: %w", err))
			return
		}
		if err := workspace.Clone(ctx, in.GitURL, dir); err != nil {
			writeDomainError(w, badRequest("clone failed: %v", err))
			return
		}
		cloned = true
		proj.GitURL = in.GitURL
		proj.RepoPath = dir
	}

	stack, testCmd := detectProject(dir)
	if proj.Stack == "" {
		proj.Stack = stack
	}
	if proj.TestCmd == "" {
		proj.TestCmd = testCmd
	}
	if proj.DefaultBranch == "" {
		if b, err := workspace.DefaultBranch(ctx, dir); err == nil {
			proj.DefaultBranch = b
		}
	}

	created, err := s.st.CreateProject(ctx, proj)
	if err != nil {
		if cloned {
			_ = os.RemoveAll(dir)
		}
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "project.create", "project", created.ID,
		map[string]any{"name": created.Name, "source": source, "stack": created.Stack})
	writeJSON(w, http.StatusCreated, created)
}

// buildNewProject validates the create body and returns the project row to insert. Policy values are
// checked for shape only; the role check happens before this call.
func buildNewProject(id, name string, in createProjectIn) (domain.Project, error) {
	if err := validateOptionalText("type", in.Type, maxProjectTypeLen); err != nil {
		return domain.Project{}, err
	}
	if err := validateOptionalText("stack", in.Stack, maxProjectTypeLen); err != nil {
		return domain.Project{}, err
	}
	if err := validateOptionalText("defaultModel", in.DefaultModel, maxShortTextBytes); err != nil {
		return domain.Project{}, err
	}
	if in.DefaultBranch != "" && !branchNameRe.MatchString(in.DefaultBranch) {
		return domain.Project{}, badRequest("defaultBranch is not a valid branch name")
	}
	if in.DefaultAgent != "" && !agentNameRe.MatchString(in.DefaultAgent) {
		return domain.Project{}, badRequest("defaultAgent is not a valid agent name")
	}
	if in.MaxConcurrent != 0 {
		if err := checkRange("maxConcurrent", in.MaxConcurrent, 1, maxMaxConcurrent); err != nil {
			return domain.Project{}, err
		}
	}
	if in.TimeoutMinutes != 0 {
		if err := checkRange("timeoutMinutes", in.TimeoutMinutes, 1, maxTimeoutMinutes); err != nil {
			return domain.Project{}, err
		}
	}
	for field, v := range map[string]string{"buildCmd": in.BuildCmd, "testCmd": in.TestCmd, "releaseCmd": in.ReleaseCmd} {
		if err := validateCommand(field, v); err != nil {
			return domain.Project{}, err
		}
	}
	if in.ReleaseStrategy != "" && !releaseStrategies[in.ReleaseStrategy] {
		return domain.Project{}, badRequest("releaseStrategy must be one of none, tag, command, rhymehub-self")
	}
	tools, err := normalizeTools(in.AllowedTools)
	if err != nil {
		return domain.Project{}, err
	}
	return domain.Project{
		ID: id, Name: name, DefaultBranch: in.DefaultBranch, Type: in.Type, Stack: in.Stack,
		BuildCmd: in.BuildCmd, TestCmd: in.TestCmd, ReleaseStrategy: in.ReleaseStrategy, ReleaseCmd: in.ReleaseCmd,
		DefaultAgent: in.DefaultAgent, DefaultModel: in.DefaultModel, MaxConcurrent: in.MaxConcurrent,
		TimeoutMinutes: in.TimeoutMinutes, AutoTaskFromRequests: in.AutoTaskFromRequests,
		AutoApprove: in.AutoApprove, AutoRelease: in.AutoRelease, AllowedTools: tools,
	}, nil
}

// ---- Discover ----

type discoverCandidate struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Stack      string `json:"stack"`
	Registered bool   `json:"registered"`
}

func (s *Server) handleDiscoverProjects(w http.ResponseWriter, r *http.Request, _ *principal) {
	var in struct {
		Root string `json:"root"`
	}
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	if !filepath.IsAbs(in.Root) {
		writeDomainError(w, badRequest("root must be an absolute path"))
		return
	}
	root := filepath.Clean(in.Root)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeDomainError(w, badRequest("root must be an existing directory"))
		return
	}
	found, err := workspace.Discover(root, maxDiscoverDepth)
	if err != nil {
		writeDomainError(w, asInvalid(err))
		return
	}
	projs, err := s.st.ListProjects(r.Context(), true)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	registered := make(map[string]bool, len(projs))
	for _, p := range projs {
		if p.RepoPath != "" {
			registered[filepath.Clean(p.RepoPath)] = true
		}
	}
	truncated := len(found) > maxDiscoverResults
	if truncated {
		found = found[:maxDiscoverResults]
	}
	cands := make([]discoverCandidate, 0, len(found))
	for _, path := range found {
		stack, _ := detectProject(path)
		cands = append(cands, discoverCandidate{
			Path: path, Name: filepath.Base(path), Stack: stack, Registered: registered[filepath.Clean(path)],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": cands, "truncated": truncated})
}

// ---- Patch ----

type patchProjectIn struct {
	Name                 *string   `json:"name"`
	DefaultBranch        *string   `json:"defaultBranch"`
	BuildCmd             *string   `json:"buildCmd"`
	DefaultAgent         *string   `json:"defaultAgent"`
	DefaultModel         *string   `json:"defaultModel"`
	MaxConcurrent        *int      `json:"maxConcurrent"`
	TimeoutMinutes       *int      `json:"timeoutMinutes"`
	AutoTaskFromRequests *bool     `json:"autoTaskFromRequests"`
	TestCmd              *string   `json:"testCmd"`
	ReleaseStrategy      *string   `json:"releaseStrategy"`
	ReleaseCmd           *string   `json:"releaseCmd"`
	AutoApprove          *bool     `json:"autoApprove"`
	AutoRelease          *bool     `json:"autoRelease"`
	AllowedTools         *[]string `json:"allowedTools"`
}

// touchesPolicy reports whether the patch sets any policy field (these require the maintainer role).
func (in patchProjectIn) touchesPolicy() bool {
	return in.BuildCmd != nil || in.TestCmd != nil || in.ReleaseStrategy != nil || in.ReleaseCmd != nil ||
		in.AutoApprove != nil || in.AutoRelease != nil || in.AllowedTools != nil
}

func (s *Server) handlePatchProject(w http.ResponseWriter, r *http.Request, p *principal) {
	var in patchProjectIn
	if !decodeJSON(w, r, &in, maxBodyBytes) {
		return
	}
	ctx := r.Context()
	proj, err := s.st.GetProject(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if in.touchesPolicy() {
		if !p.User.Role.AtLeast(domain.RoleMaintainer) {
			writeError(w, http.StatusForbidden, "forbidden", "policy fields require role "+string(domain.RoleMaintainer))
			return
		}
		if proj.IsSelf && !p.User.Role.AtLeast(domain.RoleAdmin) {
			writeError(w, http.StatusForbidden, "forbidden", "policies of the rhymehub self project require role "+string(domain.RoleAdmin))
			return
		}
	}
	changes, err := applyProjectPatch(&proj, in)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	proj.UpdatedAt = s.now()
	updated, err := s.st.UpdateProject(ctx, proj)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	s.audit(r, &p.User, "project.update", "project", updated.ID, changes)
	writeJSON(w, http.StatusOK, updated)
}

// applyProjectPatch validates the patch fields and applies them to proj. It returns the changed fields for the
// audit entry. Validation runs per field before anything is assigned, so a rejected patch changes nothing.
func applyProjectPatch(proj *domain.Project, in patchProjectIn) (map[string]any, error) {
	changes := map[string]any{}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if err := validateProjectName(n); err != nil {
			return nil, err
		}
		proj.Name = n
		changes["name"] = n
	}
	if in.DefaultBranch != nil {
		if !branchNameRe.MatchString(*in.DefaultBranch) {
			return nil, badRequest("defaultBranch is not a valid branch name")
		}
		proj.DefaultBranch = *in.DefaultBranch
		changes["defaultBranch"] = proj.DefaultBranch
	}
	if in.DefaultAgent != nil {
		if *in.DefaultAgent != "" && !agentNameRe.MatchString(*in.DefaultAgent) {
			return nil, badRequest("defaultAgent is not a valid agent name")
		}
		proj.DefaultAgent = *in.DefaultAgent
		changes["defaultAgent"] = proj.DefaultAgent
	}
	if in.DefaultModel != nil {
		if err := validateOptionalText("defaultModel", *in.DefaultModel, maxShortTextBytes); err != nil {
			return nil, err
		}
		proj.DefaultModel = *in.DefaultModel
		changes["defaultModel"] = proj.DefaultModel
	}
	if in.MaxConcurrent != nil {
		if err := checkRange("maxConcurrent", *in.MaxConcurrent, 1, maxMaxConcurrent); err != nil {
			return nil, err
		}
		proj.MaxConcurrent = *in.MaxConcurrent
		changes["maxConcurrent"] = proj.MaxConcurrent
	}
	if in.TimeoutMinutes != nil {
		if err := checkRange("timeoutMinutes", *in.TimeoutMinutes, 1, maxTimeoutMinutes); err != nil {
			return nil, err
		}
		proj.TimeoutMinutes = *in.TimeoutMinutes
		changes["timeoutMinutes"] = proj.TimeoutMinutes
	}
	if in.AutoTaskFromRequests != nil {
		proj.AutoTaskFromRequests = *in.AutoTaskFromRequests
		changes["autoTaskFromRequests"] = proj.AutoTaskFromRequests
	}
	if in.BuildCmd != nil {
		if err := validateCommand("buildCmd", *in.BuildCmd); err != nil {
			return nil, err
		}
		proj.BuildCmd = *in.BuildCmd
		changes["buildCmd"] = proj.BuildCmd
	}
	if in.TestCmd != nil {
		if err := validateCommand("testCmd", *in.TestCmd); err != nil {
			return nil, err
		}
		proj.TestCmd = *in.TestCmd
		changes["testCmd"] = proj.TestCmd
	}
	if in.ReleaseStrategy != nil {
		if !releaseStrategies[*in.ReleaseStrategy] {
			return nil, badRequest("releaseStrategy must be one of none, tag, command, rhymehub-self")
		}
		proj.ReleaseStrategy = *in.ReleaseStrategy
		changes["releaseStrategy"] = proj.ReleaseStrategy
	}
	if in.ReleaseCmd != nil {
		if err := validateCommand("releaseCmd", *in.ReleaseCmd); err != nil {
			return nil, err
		}
		proj.ReleaseCmd = *in.ReleaseCmd
		changes["releaseCmd"] = proj.ReleaseCmd
	}
	if in.AutoApprove != nil {
		proj.AutoApprove = *in.AutoApprove
		changes["autoApprove"] = proj.AutoApprove
	}
	if in.AutoRelease != nil {
		proj.AutoRelease = *in.AutoRelease
		changes["autoRelease"] = proj.AutoRelease
	}
	if in.AllowedTools != nil {
		tools, err := normalizeTools(*in.AllowedTools)
		if err != nil {
			return nil, err
		}
		proj.AllowedTools = tools
		changes["allowedTools"] = tools
	}
	return changes, nil
}

// ---- Validation helpers ----

func newProjectID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("project id: %w", err)
	}
	return "prj_" + hex.EncodeToString(b), nil
}

func validateProjectName(name string) error {
	if name == "" || len(name) > maxProjectNameLen {
		return badRequest("name must be 1-%d characters", maxProjectNameLen)
	}
	return nil
}

func validateOptionalText(field, v string, max int) error {
	if len(v) > max || strings.ContainsRune(v, 0) {
		return badRequest("%s must be at most %d bytes", field, max)
	}
	return nil
}

func validateCommand(field, v string) error {
	if len(v) > maxCmdBytes || strings.ContainsRune(v, 0) {
		return badRequest("%s must be at most %d bytes", field, maxCmdBytes)
	}
	return nil
}

func checkRange(field string, v, lo, hi int) error {
	if v < lo || v > hi {
		return badRequest("%s must be between %d and %d", field, lo, hi)
	}
	return nil
}

// normalizeTools trims tool names and refuses globally forbidden ones, which a project policy can never grant.
func normalizeTools(in []string) ([]string, error) {
	if len(in) > maxAllowedTools {
		return nil, badRequest("allowedTools may contain at most %d entries", maxAllowedTools)
	}
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > maxToolNameLen {
			return nil, badRequest("allowedTools entries must be 1-%d characters", maxToolNameLen)
		}
		if isForbiddenTool(t) {
			return nil, badRequest("tool %q is globally forbidden", t)
		}
		out = append(out, t)
	}
	return out, nil
}
