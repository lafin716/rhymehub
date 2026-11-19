// Package workspace wraps the git CLI for per-task worktrees, diffs, commits, merges, clones and repo discovery.
// Every git call uses an explicit argv (never a shell), GIT_TERMINAL_PROMPT=0, and no credential injection.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lafin716/rhymehub/internal/redact"
)

// ErrConflict is returned by Merge when the branch conflicts with the base. The base is left clean.
var ErrConflict = errors.New("workspace: merge conflict")

// ErrInvalid is returned for arguments that fail validation (bad path, bad name, unsafe URL).
var ErrInvalid = errors.New("workspace: invalid argument")

// BranchPrefix is prepended to the task id to name a task branch.
const BranchPrefix = "rhymehub/"

var taskIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// runGit executes git with explicit args in dir. It returns trimmed stdout and the exit code.
// A non-zero exit is reported as an error that includes stderr.
func runGit(ctx context.Context, dir string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimRight(stdout.String(), "\r\n")
	if err == nil {
		return out, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitCode(), fmt.Errorf("git %s: exit %d: %s", args[0], exitErr.ExitCode(),
			strings.TrimSpace(stderr.String()))
	}
	return out, -1, fmt.Errorf("git %s: %w", args[0], err)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, _, err := runGit(ctx, dir, args...)
	return out, err
}

// ValidatePath cleans p, requires it to be absolute, resolves symlinks (for the longest existing prefix) and
// requires the result to be equal to or inside one of roots (also resolved). It returns the resolved path.
func ValidatePath(p string, roots ...string) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: path must be absolute: %q", ErrInvalid, p)
	}
	clean := filepath.Clean(p)
	resolved, err := resolveExisting(clean)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("%w: no allowed roots", ErrInvalid)
	}
	for _, r := range roots {
		rr, err := resolveExisting(filepath.Clean(r))
		if err != nil {
			return "", err
		}
		if within(rr, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("%w: %s escapes allowed roots", ErrInvalid, p)
}

// resolveExisting resolves symlinks in the longest existing prefix of p and re-appends the rest.
func resolveExisting(p string) (string, error) {
	rest := ""
	cur := p
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return r, nil
			}
			return filepath.Join(r, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: resolve %s: %v", ErrInvalid, p, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func within(root, p string) bool {
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// ValidateRepo checks that dir is the top level of a git work tree.
func ValidateRepo(ctx context.Context, dir string) error {
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("%w: %s is not a git repository", ErrInvalid, dir)
	}
	a, err1 := filepath.EvalSymlinks(top)
	b, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil || a != b {
		return fmt.Errorf("%w: %s is not the top level of a repository", ErrInvalid, dir)
	}
	return nil
}

// DefaultBranch returns the repository's default branch. It tries origin/HEAD, then the current branch
// (if not detached), then "main" and "master".
func DefaultBranch(ctx context.Context, repo string) (string, error) {
	if ref, err := git(ctx, repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && ref != "" {
		return strings.TrimPrefix(ref, "origin/"), nil
	}
	if cur, err := git(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && cur != "" && cur != "HEAD" {
		return cur, nil
	}
	for _, b := range []string{"main", "master"} {
		if _, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b, nil
		}
	}
	return "", fmt.Errorf("%w: cannot detect default branch in %s", ErrInvalid, repo)
}

// CurrentBranch returns the checked-out branch name, or "" when detached.
func CurrentBranch(ctx context.Context, repo string) (string, error) {
	cur, err := git(ctx, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if cur == "HEAD" {
		return "", nil
	}
	return cur, nil
}

// TaskBranch returns the branch name for a task id, validating the id.
func TaskBranch(taskID string) (string, error) {
	if !taskIDRe.MatchString(taskID) {
		return "", fmt.Errorf("%w: task id %q", ErrInvalid, taskID)
	}
	return BranchPrefix + taskID, nil
}

// CreateWorktree creates worktreePath on a new branch rhymehub/<taskID> starting at base. The worktree path
// must not exist yet. It returns the branch name and the base commit sha.
func CreateWorktree(ctx context.Context, repo, worktreePath, taskID, base string) (string, string, error) {
	branch, err := TaskBranch(taskID)
	if err != nil {
		return "", "", err
	}
	if base == "" || strings.HasPrefix(base, "-") {
		return "", "", fmt.Errorf("%w: base %q", ErrInvalid, base)
	}
	if !filepath.IsAbs(worktreePath) {
		return "", "", fmt.Errorf("%w: worktree path must be absolute", ErrInvalid)
	}
	if _, err := os.Lstat(worktreePath); err == nil {
		return "", "", fmt.Errorf("%w: worktree path %s already exists", ErrInvalid, worktreePath)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
		return "", "", fmt.Errorf("workspace: create parent: %w", err)
	}
	if _, err := git(ctx, repo, "worktree", "add", "-b", branch, "--", worktreePath, base); err != nil {
		return "", "", err
	}
	sha, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil {
		return "", "", err
	}
	return branch, sha, nil
}

// RemoveWorktree force-removes a worktree and prunes its registration. The branch is kept.
func RemoveWorktree(ctx context.Context, repo, worktreePath string) error {
	if _, err := git(ctx, repo, "worktree", "remove", "--force", "--", worktreePath); err != nil {
		return err
	}
	_, err := git(ctx, repo, "worktree", "prune")
	return err
}

// DeleteBranch force-deletes a local branch (used after a task is cancelled and its work discarded).
func DeleteBranch(ctx context.Context, repo, branch string) error {
	if !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("%w: refusing to delete non-task branch %q", ErrInvalid, branch)
	}
	if _, err := TaskBranch(strings.TrimPrefix(branch, BranchPrefix)); err != nil {
		return err
	}
	_, err := git(ctx, repo, "branch", "-D", "--", branch)
	return err
}

// DiffResult is the change set of a worktree against its base.
type DiffResult struct {
	// Stat is `git diff --stat` output.
	Stat string
	// Patch is the full unified diff, including untracked files.
	Patch string
}

// Diff returns the changes of worktree relative to base, including committed work on the branch and
// untracked files (added as intent-to-add so they appear in the patch).
func Diff(ctx context.Context, worktree, base string) (DiffResult, error) {
	if _, err := git(ctx, worktree, "add", "-N", "--", "."); err != nil {
		return DiffResult{}, err
	}
	stat, err := git(ctx, worktree, "diff", "--stat", base)
	if err != nil {
		return DiffResult{}, err
	}
	patch, err := git(ctx, worktree, "diff", base)
	if err != nil {
		return DiffResult{}, err
	}
	return DiffResult{Stat: stat, Patch: patch}, nil
}

// CommitAll stages all changes in worktree and commits them with msg. It returns the new commit sha and
// changed=false (with an empty sha) when there was nothing to commit. Author identity comes from git config.
func CommitAll(ctx context.Context, worktree, msg string) (string, bool, error) {
	if strings.TrimSpace(msg) == "" {
		return "", false, fmt.Errorf("%w: empty commit message", ErrInvalid)
	}
	if _, err := git(ctx, worktree, "add", "-A"); err != nil {
		return "", false, err
	}
	status, err := git(ctx, worktree, "status", "--porcelain")
	if err != nil {
		return "", false, err
	}
	if status == "" {
		return "", false, nil
	}
	if _, err := git(ctx, worktree, "commit", "-m", msg); err != nil {
		return "", false, err
	}
	sha, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", false, err
	}
	return sha, true, nil
}

// Merge merges branch into base in repo with --no-ff and returns the merge commit sha. The repo must have
// base checked out and a clean tree. Conflicts are detected before any change is made (merge-tree); if a
// conflict still occurs, the merge is aborted. In both cases ErrConflict is returned and the base stays clean.
func Merge(ctx context.Context, repo, base, branch, msg string) (string, error) {
	if strings.HasPrefix(base, "-") || strings.HasPrefix(branch, "-") {
		return "", fmt.Errorf("%w: branch names", ErrInvalid)
	}
	cur, err := CurrentBranch(ctx, repo)
	if err != nil {
		return "", err
	}
	if cur != base {
		return "", fmt.Errorf("%w: %s has %q checked out, expected %q", ErrInvalid, repo, cur, base)
	}
	dirty, err := git(ctx, repo, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if dirty != "" {
		return "", fmt.Errorf("%w: base repository has uncommitted changes", ErrInvalid)
	}
	// Pre-check without touching the working tree: exit 1 means conflicts.
	_, code, err := runGit(ctx, repo, "merge-tree", "--write-tree", "--name-only", "--messages", base, branch)
	if code == 1 {
		return "", fmt.Errorf("%w: %s conflicts with %s", ErrConflict, branch, base)
	}
	if err != nil && code != 0 {
		return "", err
	}
	if msg == "" {
		msg = fmt.Sprintf("Merge %s into %s", branch, base)
	}
	if _, _, err := runGit(ctx, repo, "merge", "--no-ff", "-m", msg, branch); err != nil {
		unmerged, _ := git(ctx, repo, "diff", "--name-only", "--diff-filter=U")
		if unmerged == "" {
			return "", err
		}
		if _, _, abortErr := runGit(ctx, repo, "merge", "--abort"); abortErr != nil {
			return "", fmt.Errorf("workspace: merge conflicted and abort failed: %v", abortErr)
		}
		return "", fmt.Errorf("%w: %s conflicts with %s", ErrConflict, branch, base)
	}
	return git(ctx, repo, "rev-parse", "HEAD")
}

// ValidateCloneURL reports whether url is acceptable for Clone (checked before any filesystem work).
func ValidateCloneURL(url string) error { return validCloneURL(url) }

// validCloneURL accepts https, ssh and scp-style git@ URLs. file://, ext:: and option-like strings are refused.
func validCloneURL(u string) error {
	if u == "" || strings.HasPrefix(u, "-") || strings.ContainsAny(u, " \t\r\n") {
		return fmt.Errorf("%w: clone url %q", ErrInvalid, u)
	}
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "ssh://"), strings.HasPrefix(lower, "git@"):
	default:
		return fmt.Errorf("%w: clone url scheme not allowed: %q", ErrInvalid, u)
	}
	// Refuse embedded passwords (user:pass@host). A bare username (git@host) is allowed for ssh.
	// Credentials are never injected by rhymehub either.
	if scheme := strings.Index(u, "://"); scheme >= 0 {
		rest := u[scheme+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			if slash := strings.Index(rest, "/"); slash < 0 || at < slash {
				if strings.Contains(rest[:at], ":") {
					return fmt.Errorf("%w: clone url must not embed credentials", ErrInvalid)
				}
			}
		}
	}
	return nil
}

// Clone clones url into dir (which must not exist). No credentials are injected; authentication is whatever
// the user's git setup provides, and interactive prompts are disabled.
func Clone(ctx context.Context, url, dir string) error {
	if err := validCloneURL(url); err != nil {
		return err
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("%w: clone target must be absolute", ErrInvalid)
	}
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("%w: clone target %s exists", ErrInvalid, dir)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("workspace: create parent: %w", err)
	}
	_, err := git(ctx, filepath.Dir(dir), "clone", "--", url, dir)
	return err
}

// Discover walks root up to depth levels (0 = root only) and returns the directories that contain a .git
// entry. It does not follow symlinks, skips node_modules and hidden directories, and does not descend into
// repositories it finds.
func Discover(root string, depth int) ([]string, error) {
	root = filepath.Clean(root)
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("workspace: discover %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: discover root is not a directory", ErrInvalid)
	}
	var out []string
	var walk func(dir string, level int) error
	walk = func(dir string, level int) error {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			out = append(out, dir)
			return nil
		}
		if level >= depth {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("workspace: read %s: %w", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || name == "node_modules" {
				continue
			}
			if err := walk(filepath.Join(dir, name), level+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	return out, nil
}

// gitEnv is the environment for every git call: the scrubbed allowlist (repo hooks and config run with it, so
// server credentials must not reach them) plus SSH_AUTH_SOCK so ssh remotes still clone, and no prompts.
func gitEnv() []string {
	return append(redact.ScrubEnv(os.Environ(), []string{"SSH_AUTH_SOCK"}), "GIT_TERMINAL_PROMPT=0")
}

// GitEnv exposes gitEnv to other packages that run git directly.
func GitEnv() []string { return gitEnv() }
