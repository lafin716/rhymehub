package release

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
	"github.com/lafin716/rhymehub/internal/workspace"
)

// mergeTask merges the task branch into the project's base branch with --no-ff and records a merge event. It
// returns the merge commit. Problems with the task or the repository state come back as a *failure (nil error);
// infrastructure errors come back as error so the caller can retry.
//
// A task whose branch is already contained in the base (a previous attempt merged but did not finish) is not merged
// again; its merge commit is looked up instead, which makes the step idempotent.
func (m *Manager) mergeTask(ctx context.Context, t domain.Task, p domain.Project) (string, *failure, error) {
	repo := p.RepoPath
	if err := workspace.ValidateRepo(ctx, repo); err != nil {
		return "", failf(ReasonMergeFailed, "%v", err), nil
	}
	base := p.DefaultBranch
	if base == "" {
		b, err := workspace.DefaultBranch(ctx, repo)
		if err != nil {
			return "", failf(ReasonMergeFailed, "%v", err), nil
		}
		base = b
	}
	branch := t.Branch
	if branch == "" {
		b, err := workspace.TaskBranch(t.ID)
		if err != nil {
			return "", failf(ReasonMergeFailed, "%v", err), nil
		}
		branch = b
	}
	if _, ok, err := revParseQuiet(ctx, repo, "refs/heads/"+branch); err != nil {
		return "", nil, err
	} else if !ok {
		return "", failf(ReasonMergeFailed, "task branch %s does not exist in %s", branch, repo), nil
	}

	var sha string
	merged, err := isAncestor(ctx, repo, "refs/heads/"+branch, "refs/heads/"+base)
	if err != nil {
		return "", nil, err
	}
	if merged {
		// Resume: an earlier attempt already merged the branch. Find that merge commit and record it.
		if sha, err = findMergeCommit(ctx, repo, branch, base); err != nil {
			return "", nil, err
		}
		return m.recordMerge(ctx, t, branch, sha)
	}

	cur, err := workspace.CurrentBranch(ctx, repo)
	if err != nil {
		return "", nil, err
	}
	if cur != base {
		return "", failf(ReasonBaseNotCheckedOut, "base branch %s is not checked out in %s (checked out: %q)",
			base, repo, cur), nil
	}
	dirty, err := gitOut(ctx, repo, "status", "--porcelain")
	if err != nil {
		return "", nil, err
	}
	if dirty != "" {
		return "", failf(ReasonDirtyBase, "base repository %s has uncommitted changes; merge refused", repo), nil
	}
	files, conflict, err := conflictFiles(ctx, repo, base, branch)
	if err != nil {
		return "", nil, err
	}
	if conflict {
		return "", conflictFailure(t, base, branch, files), nil
	}

	msg := fmt.Sprintf("Merge %s into %s", branch, base)
	if t.Title != "" {
		msg += ": " + t.Title
	}
	sha, err = workspace.Merge(ctx, repo, base, branch, msg)
	switch {
	case errors.Is(err, workspace.ErrConflict):
		// The pre-check said clean but the merge still conflicted; it was aborted and the base is clean.
		files, _, _ := conflictFiles(ctx, repo, base, branch) // best effort: only used for the message
		return "", conflictFailure(t, base, branch, files), nil
	case err != nil:
		return "", failf(ReasonMergeFailed, "merge %s into %s failed: %v", branch, base, err), nil
	}
	if err := m.audit(ctx, "release.merge", "task", t.ID, map[string]any{
		"commit": sha, "branch": branch, "base": base,
	}); err != nil {
		return "", nil, err
	}
	return m.recordMerge(ctx, t, branch, sha)
}

// recordMerge stores the merge commit on the task (HeadCommit) and writes the merge event.
func (m *Manager) recordMerge(ctx context.Context, t domain.Task, branch, sha string) (string, *failure, error) {
	if _, err := m.st.AppendTaskEvent(ctx, t.ID, store.NewEvent{Type: domain.EventMerge,
		Payload: mustJSON(map[string]any{"commit": sha, "branch": branch}),
		IdemKey: "merge:" + t.ID}); err != nil {
		return "", nil, err
	}
	if _, err := m.st.MutateTask(ctx, t.ID, func(x *domain.Task) error {
		x.Branch = branch
		x.HeadCommit = sha
		return nil
	}); err != nil {
		return "", nil, err
	}
	return sha, nil, nil
}

// conflictFailure builds the failure for a merge conflict. The worktree is kept so the conflict can be inspected.
func conflictFailure(t domain.Task, base, branch string, files []string) *failure {
	if files == nil {
		files = []string{}
	}
	msg := fmt.Sprintf("merge of %s into %s conflicts", branch, base)
	if len(files) > 0 {
		msg += " in: " + strings.Join(files, ", ")
	}
	msg += "; task worktree kept at " + t.WorktreePath
	return &failure{reason: ReasonMergeConflict, msg: msg, detail: mustJSON(map[string]any{"files": files})}
}

// findMergeCommit returns the oldest merge commit on base that has branch in its ancestry. It falls back to the base
// tip when there is no such merge (a task branch with no commits of its own).
func findMergeCommit(ctx context.Context, repo, branch, base string) (string, error) {
	out, err := gitOut(ctx, repo, "rev-list", "--merges", "--ancestry-path", "--reverse",
		"refs/heads/"+branch+"..refs/heads/"+base)
	if err != nil {
		return "", err
	}
	if first, _, _ := strings.Cut(out, "\n"); first != "" {
		return first, nil
	}
	return gitOut(ctx, repo, "rev-parse", "--verify", "refs/heads/"+base)
}

// conflictFiles runs a no-side-effect merge check (git merge-tree --write-tree). It reports whether merging branch
// into base conflicts and, if so, the conflicted file names.
func conflictFiles(ctx context.Context, repo, base, branch string) ([]string, bool, error) {
	out, code, err := gitRun(ctx, repo, "merge-tree", "--write-tree", "--name-only", "--messages", base, branch)
	switch code {
	case 0:
		return nil, false, nil
	case 1:
		// Output: the resulting tree oid, the conflicted file names, a blank line, then informational messages.
		var files []string
		lines := strings.Split(out, "\n")
		for _, l := range lines[min(1, len(lines)):] {
			if l == "" {
				break
			}
			files = append(files, l)
		}
		return files, true, nil
	}
	return nil, false, err
}

// isAncestor reports whether commit a is an ancestor of (or equal to) commit b.
func isAncestor(ctx context.Context, repo, a, b string) (bool, error) {
	_, code, err := gitRun(ctx, repo, "merge-base", "--is-ancestor", a, b)
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, err
}

// revParseQuiet resolves rev to a commit-ish sha. ok is false when rev does not exist.
func revParseQuiet(ctx context.Context, repo, rev string) (string, bool, error) {
	out, code, err := gitRun(ctx, repo, "rev-parse", "--verify", "--quiet", rev)
	switch code {
	case 0:
		return out, true, nil
	case 1:
		return "", false, nil
	}
	return "", false, err
}

// gitRun runs git with explicit argv in dir. It returns trimmed stdout and the exit code (-1 when git did not run).
// A non-zero exit is returned as an error that includes stderr; callers that expect specific exit codes check code
// first.
func gitRun(ctx context.Context, dir string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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

// gitOut runs git and returns its trimmed stdout; any non-zero exit is an error.
func gitOut(ctx context.Context, repo string, args ...string) (string, error) {
	out, _, err := gitRun(ctx, repo, args...)
	return out, err
}
