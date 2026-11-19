package release

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/signing"
	"github.com/lafin716/rhymehub/internal/store"
	"github.com/lafin716/rhymehub/internal/workspace"
)

// runIn runs git in dir and fails the test on error.
func runIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// makeRepo creates a real git repository on branch main with one commit. The path is resolved through
// EvalSymlinks so git's reported paths match on macOS, where TempDir sits behind a symlink.
func makeRepo(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runIn(t, repo, "init", "-b", "main")
	runIn(t, repo, "config", "user.name", "Test")
	runIn(t, repo, "config", "user.email", "test@example.com")
	runIn(t, repo, "config", "commit.gpgsign", "false")
	writeFile(t, filepath.Join(repo, "README.md"), "hello\n")
	runIn(t, repo, "add", "-A")
	runIn(t, repo, "commit", "-m", "init")
	return repo
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

type fixture struct {
	t     *testing.T
	st    *store.Store
	paths config.Paths
	repo  string
	prj   domain.Project
	mgr   *Manager
	ctx   context.Context
}

// newFixture builds a temp RHYMEHUB_HOME, a migrated store, a real git repo and one project.
func newFixture(t *testing.T, strategy, releaseCmd string, runner CommandRunner) *fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	paths := config.NewPaths(home)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	repo := makeRepo(t)
	prj, err := st.CreateProject(ctx, domain.Project{Name: "demo", RepoPath: repo, DefaultBranch: "main",
		ReleaseStrategy: strategy, ReleaseCmd: releaseCmd})
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	mgr := New(Options{Store: st, Paths: paths, Config: config.Default(), RunCmd: runner,
		Now: func() time.Time { return fixedNow }})
	return &fixture{t: t, st: st, paths: paths, repo: repo, prj: prj, mgr: mgr, ctx: ctx}
}

// approvedTask creates a task with its own worktree from the base branch, applies edit inside the worktree,
// commits the result and walks the task to APPROVED.
func (f *fixture) approvedTask(name string, edit func(wt string)) domain.Task {
	f.t.Helper()
	id := "tsk_" + name
	wt := filepath.Join(f.paths.WorktreesDir(), id)
	branch, sha, err := workspace.CreateWorktree(f.ctx, f.repo, wt, id, "main")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.CreateTask(f.ctx, domain.Task{ID: id, ProjectID: f.prj.ID, Title: name, Prompt: name,
		Agent: "claude", CreatedBy: "test", Branch: branch, WorktreePath: wt, BaseCommit: sha}); err != nil {
		f.t.Fatal(err)
	}
	for _, s := range []domain.TaskState{domain.StateQueued, domain.StatePreparing, domain.StateRunning,
		domain.StateValidating, domain.StateReviewReady, domain.StateApproved} {
		if _, err := f.st.TransitionTask(f.ctx, id, nil, s, &store.EventInput{Reason: "test"}); err != nil {
			f.t.Fatalf("transition to %s: %v", s, err)
		}
	}
	if edit != nil {
		edit(wt)
	}
	if _, _, err := workspace.CommitAll(f.ctx, wt, "feat: "+name); err != nil {
		f.t.Fatal(err)
	}
	return f.task(id)
}

func (f *fixture) task(id string) domain.Task {
	f.t.Helper()
	tk, err := f.st.GetTask(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return tk
}

func (f *fixture) events(taskID string) []domain.TaskEvent {
	f.t.Helper()
	evs, err := f.st.ListEventsSince(f.ctx, taskID, 0, 1000)
	if err != nil {
		f.t.Fatal(err)
	}
	return evs
}

func countEvents(evs []domain.TaskEvent, typ domain.EventType) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func (f *fixture) releases() []domain.Release {
	f.t.Helper()
	rels, err := f.st.ListReleases(f.ctx, f.prj.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return rels
}

func (f *fixture) run(taskID string) error {
	return f.mgr.MergeAndRelease(f.ctx, taskID)
}

func TestNoneStrategyCompletesAndIsIdempotent(t *testing.T) {
	f := newFixture(t, StrategyNone, "", nil)
	tk := f.approvedTask("none1", func(wt string) { writeFile(t, filepath.Join(wt, "feature.txt"), "one\n") })

	if err := f.run(tk.ID); err != nil {
		t.Fatalf("first run: %v", err)
	}
	got := f.task(tk.ID)
	if got.State != domain.StateCompleted {
		t.Fatalf("state = %s, want COMPLETED", got.State)
	}
	if got.HeadCommit == "" {
		t.Fatal("HeadCommit not recorded")
	}
	if n := len(f.releases()); n != 0 {
		t.Fatalf("releases = %d, want 0", n)
	}
	if err := f.run(tk.ID); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if n := countEvents(f.events(tk.ID), domain.EventMerge); n != 1 {
		t.Fatalf("merge events = %d, want 1", n)
	}
	if n := len(f.releases()); n != 0 {
		t.Fatalf("releases after rerun = %d, want 0", n)
	}
}

func TestTagStrategyVersionsAndNotes(t *testing.T) {
	f := newFixture(t, StrategyTag, "", nil)
	tk1 := f.approvedTask("tag1", func(wt string) { writeFile(t, filepath.Join(wt, "a.txt"), "1\n") })
	if err := f.run(tk1.ID); err != nil {
		t.Fatalf("tag1: %v", err)
	}
	got := f.task(tk1.ID)
	if got.State != domain.StateCompleted {
		t.Fatalf("state = %s, want COMPLETED", got.State)
	}
	if got := runIn(t, f.repo, "cat-file", "-t", "v0.1.0"); got != "tag" {
		t.Fatalf("v0.1.0 object type = %q, want annotated tag", got)
	}
	rels := f.releases()
	if len(rels) != 1 || rels[0].Version != "0.1.0" || rels[0].Tag != "v0.1.0" ||
		rels[0].Status != domain.ReleasePublished {
		t.Fatalf("releases = %+v, want one published 0.1.0", rels)
	}
	if !strings.Contains(rels[0].Notes, "- feat: tag1") {
		t.Fatalf("notes = %q, want to contain \"- feat: tag1\"", rels[0].Notes)
	}
	if got.ReleaseID != rels[0].ID {
		t.Fatalf("task ReleaseID = %q, want %q", got.ReleaseID, rels[0].ID)
	}

	// The second release has no request type, so it bumps the patch and notes only cover new commits.
	tk2 := f.approvedTask("tag2", func(wt string) { writeFile(t, filepath.Join(wt, "b.txt"), "2\n") })
	if err := f.run(tk2.ID); err != nil {
		t.Fatalf("tag2: %v", err)
	}
	rels = f.releases()
	if len(rels) != 2 {
		t.Fatalf("releases = %d, want 2", len(rels))
	}
	second := rels[0] // newest first
	if second.Version != "0.1.1" || second.Tag != "v0.1.1" {
		t.Fatalf("second release = %s %s, want 0.1.1 v0.1.1", second.Version, second.Tag)
	}
	if strings.Contains(second.Notes, "tag1") || !strings.Contains(second.Notes, "- feat: tag2") {
		t.Fatalf("second notes = %q, want only tag2 commit", second.Notes)
	}

	// Re-run: nothing new is created.
	if err := f.run(tk1.ID); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if n := len(f.releases()); n != 2 {
		t.Fatalf("releases after rerun = %d, want 2", n)
	}
	if n := countEvents(f.events(tk1.ID), domain.EventRelease); n != 1 {
		t.Fatalf("release events = %d, want 1", n)
	}
}

func TestCommandStrategyRunsConfiguredCommand(t *testing.T) {
	f := newFixture(t, StrategyCommand, `echo "$VERSION $RHYMEHUB_VERSION" > ../version.txt; echo ran`, nil)
	tk := f.approvedTask("cmd1", func(wt string) { writeFile(t, filepath.Join(wt, "c.txt"), "c\n") })

	if err := f.run(tk.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := f.task(tk.ID); got.State != domain.StateCompleted {
		t.Fatalf("state = %s, want COMPLETED", got.State)
	}
	b, err := os.ReadFile(filepath.Join(f.repo, "..", "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != "0.1.0 0.1.0" {
		t.Fatalf("command saw versions %q, want \"0.1.0 0.1.0\"", got)
	}
	rels := f.releases()
	if len(rels) != 1 || rels[0].Status != domain.ReleasePublished || !strings.Contains(rels[0].Log, "ran") {
		t.Fatalf("releases = %+v, want published with log containing \"ran\"", rels)
	}
}

func TestCommandStrategyFailureKeepsLogAndScrubsEnv(t *testing.T) {
	var gotEnv []string
	runner := func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
		gotEnv = env
		return []byte("partial output"), errors.New("exit status 3")
	}
	f := newFixture(t, StrategyCommand, "make release", runner)
	tk := f.approvedTask("cmd2", func(wt string) { writeFile(t, filepath.Join(wt, "d.txt"), "d\n") })

	err := f.run(tk.ID)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	got := f.task(tk.ID)
	if got.State != domain.StateFailed || got.FailureReason != ReasonReleaseFailed {
		t.Fatalf("task = %s %q, want FAILED release_failed", got.State, got.FailureReason)
	}
	rels := f.releases()
	if len(rels) != 1 || rels[0].Status != domain.ReleaseFailed || !strings.Contains(rels[0].Log, "partial output") {
		t.Fatalf("releases = %+v, want failed with log \"partial output\"", rels)
	}
	for _, kv := range gotEnv {
		if strings.HasPrefix(kv, config.EnvHome+"=") {
			t.Fatalf("release command env contains %s", config.EnvHome)
		}
	}
}

func TestMergeConflictFailsAndKeepsWorktree(t *testing.T) {
	f := newFixture(t, StrategyNone, "", nil)
	tk := f.approvedTask("conf", func(wt string) { writeFile(t, filepath.Join(wt, "README.md"), "task side\n") })
	writeFile(t, filepath.Join(f.repo, "README.md"), "base side\n")
	runIn(t, f.repo, "commit", "-am", "base change")

	err := f.run(tk.ID)
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	got := f.task(tk.ID)
	if got.State != domain.StateFailed || got.FailureReason != ReasonMergeConflict {
		t.Fatalf("task = %s %q, want FAILED merge_conflict", got.State, got.FailureReason)
	}
	if !strings.Contains(got.Error, "README.md") {
		t.Fatalf("task error = %q, want file list with README.md", got.Error)
	}
	if _, err := os.Stat(tk.WorktreePath); err != nil {
		t.Fatalf("worktree not kept: %v", err)
	}
	if st := runIn(t, f.repo, "status", "--porcelain"); st != "" {
		t.Fatalf("base not clean after conflict: %q", st)
	}
	if n := countEvents(f.events(tk.ID), domain.EventMerge); n != 0 {
		t.Fatalf("merge events = %d, want 0", n)
	}
}

func TestDirtyBaseFails(t *testing.T) {
	f := newFixture(t, StrategyNone, "", nil)
	tk := f.approvedTask("dirty", func(wt string) { writeFile(t, filepath.Join(wt, "feature.txt"), "x\n") })
	writeFile(t, filepath.Join(f.repo, "scratch.txt"), "untracked\n")

	if err := f.run(tk.ID); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	got := f.task(tk.ID)
	if got.State != domain.StateFailed || got.FailureReason != ReasonDirtyBase {
		t.Fatalf("task = %s %q, want FAILED dirty_base", got.State, got.FailureReason)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("feature.txt merged into dirty base: %v", err)
	}
}

func TestBaseNotCheckedOutFails(t *testing.T) {
	f := newFixture(t, StrategyNone, "", nil)
	tk := f.approvedTask("detour", func(wt string) { writeFile(t, filepath.Join(wt, "f.txt"), "f\n") })
	runIn(t, f.repo, "checkout", "-b", "other")

	if err := f.run(tk.ID); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	if got := f.task(tk.ID); got.FailureReason != ReasonBaseNotCheckedOut {
		t.Fatalf("reason = %q, want base_not_checked_out", got.FailureReason)
	}
}

func TestResumeAfterMergeDoesNotMergeTwice(t *testing.T) {
	f := newFixture(t, StrategyNone, "", nil)
	tk := f.approvedTask("resume", func(wt string) { writeFile(t, filepath.Join(wt, "r.txt"), "r\n") })

	// Simulate a crash after the merge commit was made but before the task moved on.
	if _, err := f.st.TransitionTask(f.ctx, tk.ID, []domain.TaskState{domain.StateApproved}, domain.StateMerging,
		&store.EventInput{Reason: "simulated"}); err != nil {
		t.Fatal(err)
	}
	runIn(t, f.repo, "merge", "--no-ff", "-m", "manual merge", TaskBranchFor(tk.ID))

	if err := f.run(tk.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := f.task(tk.ID); got.State != domain.StateCompleted {
		t.Fatalf("state = %s, want COMPLETED", got.State)
	}
	if n := countEvents(f.events(tk.ID), domain.EventMerge); n != 1 {
		t.Fatalf("merge events = %d, want 1", n)
	}
	if n := len(runIn(t, f.repo, "rev-list", "--merges", "HEAD")); n == 0 {
		t.Fatal("no merge commit on base")
	}
}

// TaskBranchFor returns the branch name workspace uses for a task.
func TaskBranchFor(taskID string) string {
	b, _ := workspace.TaskBranch(taskID)
	return b
}

func TestNextVersionTable(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		tags   []string
		change string
		want   string
	}{
		{"no tags feature", nil, "feature", "0.1.0"},
		{"no tags patch", nil, "patch", "0.1.0"},
		{"bugfix bumps patch", []string{"v0.1.3"}, "bugfix", "0.1.4"},
		{"feature bumps minor", []string{"v0.1.3"}, "feature", "0.2.0"},
		{"feature across major digits", []string{"v1.2.9"}, "feature", "1.3.0"},
		{"numeric not lexical", []string{"v0.9.0", "v0.10.0"}, "bugfix", "0.10.1"},
		{"invalid tags ignored", []string{"vnext", "v1.2", "0.5.0"}, "bugfix", "0.1.0"},
		{"empty change type is patch", []string{"v2.0.0"}, "", "2.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := makeRepo(t)
			for _, tag := range tc.tags {
				runIn(t, repo, "tag", tag)
			}
			m := New(Options{})
			got, err := m.NextVersion(ctx, repo, tc.change)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("NextVersion = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestRhymehubSelfPublishesSignedArtifacts(t *testing.T) {
	f := newFixture(t, StrategySelf, "", nil)
	priv := filepath.Join(f.paths.SecretsDir(), signing.PrivateKeyFileName)
	pubPath := filepath.Join(f.paths.KeysDir(), signing.PublicKeyFileName)
	if _, err := signing.GenerateKeyPair(priv, pubPath); err != nil {
		t.Fatal(err)
	}

	// Commit a stub build script first so the task branch inherits it. It writes two tarballs into OUTDIR.
	script := `#!/bin/sh
set -e
for target in darwin_arm64 linux_amd64; do
  printf 'payload %s %s\n' "$1" "$target" > "$2/rhymehub_$1_$target.tar.gz"
done
`
	scriptPath := filepath.Join(f.repo, "scripts", "build-release.sh")
	writeFile(t, scriptPath, script)
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatal(err)
	}
	runIn(t, f.repo, "add", "-A")
	runIn(t, f.repo, "commit", "-m", "add build script")

	tk := f.approvedTask("self1", func(wt string) { writeFile(t, filepath.Join(wt, "feature.txt"), "one\n") })
	if err := f.run(tk.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := f.task(tk.ID); got.State != domain.StateCompleted {
		t.Fatalf("state = %s, want COMPLETED", got.State)
	}
	rels := f.releases()
	if len(rels) != 1 || rels[0].Status != domain.ReleasePublished || rels[0].Version != "0.1.0" {
		t.Fatalf("releases = %+v, want one published 0.1.0", rels)
	}
	if len(rels[0].Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(rels[0].Artifacts))
	}

	pub, err := signing.LoadPublicKey(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rels[0].Artifacts {
		digest, _, err := signing.SHA256File(a.Path)
		if err != nil {
			t.Fatal(err)
		}
		if digest != a.SHA256 {
			t.Fatalf("%s sha256 = %s, row says %s", a.Name, digest, a.SHA256)
		}
		if err := signing.VerifyFile(pub, a.Path, a.SHA256, a.Signature); err != nil {
			t.Fatalf("verify %s: %v", a.Name, err)
		}
		if filepath.Dir(a.Path) != f.paths.ReleasesDir() {
			t.Fatalf("artifact %s not in releases dir: %s", a.Name, a.Path)
		}
	}

	feed, err := f.mgr.LatestFeed(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if feed.Latest != "0.1.0" {
		t.Fatalf("feed latest = %q, want 0.1.0", feed.Latest)
	}
	rel, art, err := feed.ArtifactFor("0.1.0", "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.1.0" || art.URL != "rhymehub_0.1.0_darwin_arm64.tar.gz" {
		t.Fatalf("feed artifact = %+v", art)
	}
	if err := signing.VerifyDigest(pub, art.SHA256, art.Signature); err != nil {
		t.Fatalf("feed signature does not verify: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.paths.ReleasesDir(), ".build", "0.1.0")); !os.IsNotExist(err) {
		t.Fatalf("stage dir not removed: %v", err)
	}
}

func TestRhymehubSelfMissingKeyFailsBeforeMerge(t *testing.T) {
	f := newFixture(t, StrategySelf, "", nil)
	tk := f.approvedTask("nokey", func(wt string) { writeFile(t, filepath.Join(wt, "feature.txt"), "x\n") })

	if err := f.run(tk.ID); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
	got := f.task(tk.ID)
	if got.State != domain.StateFailed || got.FailureReason != ReasonReleaseFailed {
		t.Fatalf("task = %s %q, want FAILED release_failed", got.State, got.FailureReason)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("feature.txt merged despite missing key: %v", err)
	}
	feed, err := f.mgr.LatestFeed(f.ctx)
	if err != nil {
		t.Fatalf("LatestFeed: %v", err)
	}
	if feed.Name != feedName {
		t.Fatalf("feed name = %q, want %q", feed.Name, feedName)
	}
}
