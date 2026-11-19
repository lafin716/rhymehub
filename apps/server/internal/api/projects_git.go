package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

const (
	gitReadTimeout = 10 * time.Second
	gitLogLimit    = 20
	gitFieldSep    = "\x1f"
	gitLogFormat   = "--pretty=format:%H%x1f%s%x1f%an%x1f%aI"
)

// projectGit is the git block of a project (docs/API_SPEC.md). Available is false when the repository cannot be
// read; the other fields are then zero. Ahead is nil when there is no upstream branch.
type projectGit struct {
	Available bool   `json:"available"`
	Branch    string `json:"branch"`
	Head      string `json:"head"`
	Dirty     bool   `json:"dirty"`
	Ahead     *int   `json:"ahead,omitempty"`
}

// gitCommit is one entry of recentCommits on GET /projects/{id}/git.
type gitCommit struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	At      string `json:"at"`
}

// projectGitDetail is the response of GET /projects/{id}/git.
type projectGitDetail struct {
	projectGit
	RecentCommits []gitCommit `json:"recentCommits"`
}

// runGitRO runs a fixed read-only git command in dir with no terminal prompts and a minimal environment.
// Callers pass literal argument lists only; no request text reaches argv.
func runGitRO(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
	defer cancel()
	full := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
	}
	out, err := cmd.Output()
	return string(out), err
}

// readGitState reads branch, head, dirty and ahead. It never returns an error: any git failure yields
// Available false (or the missing field left zero), so a broken repository cannot fail the API call.
func readGitState(ctx context.Context, dir string) projectGit {
	var g projectGit
	if dir == "" {
		return g
	}
	head, err := runGitRO(ctx, dir, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return g
	}
	g.Available = true
	g.Head = strings.TrimSpace(head)
	if b, err := runGitRO(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD"); err == nil {
		g.Branch = strings.TrimSpace(b) // empty when HEAD is detached
	}
	if st, err := runGitRO(ctx, dir, "status", "--porcelain"); err == nil {
		g.Dirty = strings.TrimSpace(st) != ""
	}
	if ab, err := runGitRO(ctx, dir, "rev-list", "--count", "@{u}..HEAD"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(ab)); err == nil {
			g.Ahead = &n
		}
	}
	return g
}

// readRecentCommits returns up to n commits of HEAD. Git errors yield an empty list.
func readRecentCommits(ctx context.Context, dir string, n int) []gitCommit {
	out := []gitCommit{}
	if dir == "" {
		return out
	}
	raw, err := runGitRO(ctx, dir, "log", "-n", strconv.Itoa(n), gitLogFormat)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.SplitN(line, gitFieldSep, 4)
		if len(parts) != 4 {
			continue
		}
		out = append(out, gitCommit{Hash: parts[0], Subject: parts[1], Author: parts[2], At: parts[3]})
	}
	return out
}

// reposDir is the managed clone directory for gitUrl projects: $RHYMEHUB_HOME/repos.
func (s *Server) reposDir() string {
	return filepath.Join(s.paths.Root, "repos")
}

// projectDir returns the working copy of a project: the registered repoPath, or the managed clone.
func (s *Server) projectDir(p domain.Project) string {
	if p.RepoPath != "" {
		return p.RepoPath
	}
	if p.GitURL != "" && s.paths.Root != "" {
		return filepath.Join(s.reposDir(), p.ID)
	}
	return ""
}

// detectProject guesses the stack and test command from marker files in dir. Unknown layouts give "" for both.
func detectProject(dir string) (stack, testCmd string) {
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	switch {
	case exists("go.mod"):
		return "go", "go test ./..."
	case exists("package.json"):
		pm := "npm"
		if exists("pnpm-lock.yaml") || exists("pnpm-workspace.yaml") {
			pm = "pnpm"
		}
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			_ = json.Unmarshal(b, &pkg)
		}
		if pkg.Scripts["test"] != "" {
			testCmd = pm + " test"
		}
		return "node", testCmd
	case exists("gradlew"):
		return "gradle", "./gradlew test"
	case exists("Cargo.toml"):
		return "rust", "cargo test"
	case exists("pyproject.toml"), exists("requirements.txt"):
		return "python", ""
	}
	return "", ""
}

// handleProjectGit serves GET /projects/{id}/git. It is read-only and reports git problems as available=false.
func (s *Server) handleProjectGit(w http.ResponseWriter, r *http.Request, _ *principal) {
	ctx := r.Context()
	p, err := s.st.GetProject(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	dir := s.projectDir(p)
	g := readGitState(ctx, dir)
	detail := projectGitDetail{projectGit: g, RecentCommits: []gitCommit{}}
	if g.Available {
		detail.RecentCommits = readRecentCommits(ctx, dir, gitLogLimit)
	}
	writeJSON(w, http.StatusOK, detail)
}
