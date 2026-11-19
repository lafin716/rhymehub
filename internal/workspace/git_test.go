package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// newRepo creates a repository on branch main with one commit and a local identity.
func newRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mustGit(t, ctx, dir, "init", "-q", "-b", "main")
	mustGit(t, ctx, dir, "config", "user.name", "rhymehub test")
	mustGit(t, ctx, dir, "config", "user.email", "test@example.invalid")
	mustGit(t, ctx, dir, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(dir, "README.md"), "hello\n")
	mustGit(t, ctx, dir, "add", "-A")
	mustGit(t, ctx, dir, "commit", "-q", "-m", "init")
	return dir
}

func mustGit(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	out, err := git(ctx, dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a", "b")
	outside := filepath.Join(filepath.Dir(root), "elsewhere")

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"inside root", inside, false},
		{"root itself", root, false},
		{"dotdot escape", filepath.Join(root, "..", "x"), true},
		{"outside", outside, true},
		{"relative", "rel/path", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidatePath(tc.path, root)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidatePath(%q) err = %v, wantErr %v", tc.path, err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Fatalf("error must wrap ErrInvalid: %v", err)
			}
		})
	}
}

func TestValidatePathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported: ", err)
	}
	if _, err := ValidatePath(filepath.Join(link, "file"), root); err == nil {
		t.Fatal("symlink pointing outside root must be rejected")
	}
}

func TestValidatePathNoRoots(t *testing.T) {
	if _, err := ValidatePath(t.TempDir()); err == nil {
		t.Fatal("expected error without roots")
	}
}

func TestValidateRepoAndDefaultBranch(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := ValidateRepo(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepo(ctx, t.TempDir()); err == nil {
		t.Fatal("non-repo must fail")
	}
	b, err := DefaultBranch(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if b != "main" {
		t.Fatalf("DefaultBranch = %q", b)
	}
}

func TestTaskBranchValidation(t *testing.T) {
	if b, err := TaskBranch("task_01"); err != nil || b != "rhymehub/task_01" {
		t.Fatalf("TaskBranch = %q, %v", b, err)
	}
	for _, bad := range []string{"", "../x", "-rf", "a b", "a/b"} {
		if _, err := TaskBranch(bad); err == nil {
			t.Errorf("TaskBranch(%q) should fail", bad)
		}
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt", "task1")

	branch, baseSHA, err := CreateWorktree(ctx, repo, wt, "task1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "rhymehub/task1" || len(baseSHA) < 7 {
		t.Fatalf("branch=%q base=%q", branch, baseSHA)
	}
	if _, err := os.Stat(filepath.Join(wt, "README.md")); err != nil {
		t.Fatalf("worktree not checked out: %v", err)
	}

	// Modify tracked file and add an untracked file.
	writeFile(t, filepath.Join(wt, "README.md"), "hello world\n")
	writeFile(t, filepath.Join(wt, "new.txt"), "new file\n")

	d, err := Diff(ctx, wt, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Stat, "README.md") || !strings.Contains(d.Stat, "new.txt") {
		t.Fatalf("stat missing files: %q", d.Stat)
	}
	if !strings.Contains(d.Patch, "+hello world") || !strings.Contains(d.Patch, "+new file") {
		t.Fatalf("patch missing content: %q", d.Patch)
	}

	sha, changed, err := CommitAll(ctx, wt, "task1 work")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || sha == "" {
		t.Fatalf("expected a commit, got changed=%v sha=%q", changed, sha)
	}
	_, changed, err = CommitAll(ctx, wt, "nothing")
	if err != nil || changed {
		t.Fatalf("empty commit: changed=%v err=%v", changed, err)
	}
	if _, _, err := CommitAll(ctx, wt, "  "); err == nil {
		t.Fatal("empty message must fail")
	}

	mergeSHA, err := Merge(ctx, repo, "main", branch, "merge task1")
	if err != nil {
		t.Fatal(err)
	}
	if mergeSHA == "" {
		t.Fatal("empty merge sha")
	}
	if got := mustGit(t, ctx, repo, "show", "main:new.txt"); got != "new file" {
		t.Fatalf("merged content = %q", got)
	}

	if err := RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if err := DeleteBranch(ctx, repo, branch); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWorktreeRejectsExistingPathAndBadInput(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	existing := t.TempDir()
	if _, _, err := CreateWorktree(ctx, repo, existing, "t1", "main"); err == nil {
		t.Fatal("existing path must be rejected")
	}
	if _, _, err := CreateWorktree(ctx, repo, filepath.Join(t.TempDir(), "w"), "bad id", "main"); err == nil {
		t.Fatal("bad task id must be rejected")
	}
	if _, _, err := CreateWorktree(ctx, repo, "relative/wt", "t2", "main"); err == nil {
		t.Fatal("relative worktree path must be rejected")
	}
	if _, _, err := CreateWorktree(ctx, repo, filepath.Join(t.TempDir(), "w"), "t3", "--evil"); err == nil {
		t.Fatal("option-like base must be rejected")
	}
}

func TestDeleteBranchRefusesNonTaskBranch(t *testing.T) {
	repo := newRepo(t)
	if err := DeleteBranch(context.Background(), repo, "main"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestMergeConflictLeavesBaseClean(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	wt := filepath.Join(t.TempDir(), "wt")
	branch, _, err := CreateWorktree(ctx, repo, wt, "conf1", "main")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "README.md"), "from task\n")
	if _, _, err := CommitAll(ctx, wt, "task edit"); err != nil {
		t.Fatal(err)
	}

	// Conflicting change on base.
	writeFile(t, filepath.Join(repo, "README.md"), "from base\n")
	mustGit(t, ctx, repo, "commit", "-qam", "base edit")
	headBefore := mustGit(t, ctx, repo, "rev-parse", "HEAD")

	_, err = Merge(ctx, repo, "main", branch, "")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if mustGit(t, ctx, repo, "rev-parse", "HEAD") != headBefore {
		t.Fatal("base HEAD moved after conflict")
	}
	if st := mustGit(t, ctx, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("base not clean after conflict: %q", st)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("MERGE_HEAD left behind")
	}
}

func TestMergeRequiresBaseCheckedOutAndClean(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	mustGit(t, ctx, repo, "checkout", "-q", "-b", "other")
	if _, err := Merge(ctx, repo, "main", "other", ""); err == nil {
		t.Fatal("merge must require base checked out")
	}
	mustGit(t, ctx, repo, "checkout", "-q", "main")
	writeFile(t, filepath.Join(repo, "dirty.txt"), "x")
	if _, err := Merge(ctx, repo, "main", "other", ""); err == nil {
		t.Fatal("merge must refuse a dirty base")
	}
}

func TestValidCloneURL(t *testing.T) {
	ok := []string{"https://example.com/a/b.git", "ssh://git@example.com/a.git", "git@example.com:a/b.git"}
	bad := []string{"", "-oProxyCommand=x", "file:///etc", "ext::sh -c x", "https://user:pass@example.com/a.git", "https://a b"}
	for _, u := range ok {
		if err := validCloneURL(u); err != nil {
			t.Errorf("validCloneURL(%q) unexpected error: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := validCloneURL(u); err == nil {
			t.Errorf("validCloneURL(%q) should fail", u)
		}
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("a")
	mk("b/c")
	mk("deep/x/y/z")
	mk("node_modules/skip")
	got, err := Discover(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, g := range got {
		rel, _ := filepath.Rel(root, g)
		found[rel] = true
	}
	if !found["a"] || !found["b/c"] {
		t.Fatalf("missing repos: %v", found)
	}
	if found["deep/x/y/z"] || found["node_modules/skip"] {
		t.Fatalf("depth or ignore rules violated: %v", found)
	}
}
