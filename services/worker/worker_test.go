package worker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adapters "github.com/lafin716/rhymehub/packages/agent-adapters"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
	"github.com/lafin716/rhymehub/internal/workspace"
)

// fixture is a temp git repo, a temp RHYMEHUB_HOME layout and a migrated store with one project.
// Nothing here touches the real ~/.rhymehub.
type fixture struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	paths   config.Paths
	project domain.Project
}

func newFixture(t *testing.T, testCmd string) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()

	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "symbolic-ref", "HEAD", "refs/heads/main")
	gitRun(t, repo, "config", "user.name", "rhymehub test")
	gitRun(t, repo, "config", "user.email", "test@example.com")
	gitRun(t, repo, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-q", "-m", "init")

	paths := config.NewPaths(filepath.Join(root, "rhymehub-home"))
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(paths.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProject(ctx, domain.Project{
		Name: "demo", RepoPath: repo, TestCmd: testCmd, TimeoutMinutes: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: ctx, st: st, paths: paths, project: p}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newTask creates a task and moves it to PREPARING, the state the scheduler hands to a worker.
func (f *fixture) newTask(prompt string, maxAttempts int) string {
	f.t.Helper()
	task, err := f.st.CreateTask(f.ctx, domain.Task{
		ProjectID: f.project.ID, Title: "Demo task", Prompt: prompt, Agent: "fake", MaxAttempts: maxAttempts,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.transition(task.ID, domain.StateQueued)
	f.transition(task.ID, domain.StatePreparing)
	return task.ID
}

func (f *fixture) transition(id string, to domain.TaskState) {
	f.t.Helper()
	if _, err := f.st.TransitionTask(f.ctx, id, nil, to, nil); err != nil {
		f.t.Fatalf("transition %s to %s: %v", id, to, err)
	}
}

func (f *fixture) opts(taskID string, a adapters.Adapter) Options {
	return Options{
		Store:          f.st,
		Paths:          f.paths,
		TaskID:         taskID,
		Adapter:        a,
		Environ:        []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.paths.Root, "LANG=C"},
		HeartbeatEvery: 10 * time.Millisecond,
	}
}

func (f *fixture) get(id string) domain.Task {
	f.t.Helper()
	task, err := f.st.GetTask(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return task
}

// expect asserts the task state and returns the task.
func (f *fixture) expect(id string, want domain.TaskState) domain.Task {
	f.t.Helper()
	task := f.get(id)
	if task.State != want {
		f.t.Fatalf("state = %s, want %s (failure_reason=%q error=%q)", task.State, want, task.FailureReason, task.Error)
	}
	return task
}

func (f *fixture) waitState(id string, want domain.TaskState, within time.Duration) {
	f.t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f.get(id).State == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("task %s did not reach %s within %s", id, want, within)
}

func (f *fixture) events(id string) []domain.TaskEvent {
	f.t.Helper()
	evs, err := f.st.ListEventsSince(f.ctx, id, 0, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	return evs
}

func hasEvent(evs []domain.TaskEvent, typ domain.EventType) bool {
	for _, e := range evs {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// recorder wraps an adapter and records the options of every CreateSession call.
type recorder struct {
	adapters.Adapter
	sessions []adapters.SessionOptions
}

func (r *recorder) CreateSession(ctx context.Context, opts adapters.SessionOptions) (*adapters.Session, error) {
	r.sessions = append(r.sessions, opts)
	return r.Adapter.CreateSession(ctx, opts)
}

func TestHappyPath(t *testing.T) {
	f := newFixture(t, "")
	id := f.newTask("FAKE:write hello.txt hi there", 0)

	if err := Run(f.ctx, f.opts(id, adapters.NewFake())); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := f.expect(id, domain.StateReviewReady)

	wantBranch, err := workspace.TaskBranch(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Branch != wantBranch {
		t.Errorf("branch = %q, want %q", task.Branch, wantBranch)
	}
	if want := filepath.Join(f.paths.WorktreesDir(), id); task.WorktreePath != want {
		t.Errorf("worktree = %q, want %q", task.WorktreePath, want)
	}
	if task.BaseCommit == "" || task.HeadCommit == "" || task.HeadCommit == task.BaseCommit {
		t.Errorf("commits: base=%q head=%q, want a new head commit", task.BaseCommit, task.HeadCommit)
	}
	if !strings.Contains(task.DiffStat, "hello.txt") {
		t.Errorf("diff_stat = %q, want it to mention hello.txt", task.DiffStat)
	}
	if task.TestStatus != "skipped" {
		t.Errorf("test_status = %q, want skipped (no test command)", task.TestStatus)
	}
	if task.Turns != 1 || task.SessionID == "" {
		t.Errorf("turns=%d session=%q, want 1 and a session id", task.Turns, task.SessionID)
	}
	if task.WorkerPID == 0 {
		t.Errorf("worker_pid not recorded")
	}
	got, err := os.ReadFile(filepath.Join(task.WorktreePath, "hello.txt"))
	if err != nil || string(got) != "hi there" {
		t.Fatalf("worktree file = %q, %v; want %q", got, err, "hi there")
	}
	evs := f.events(id)
	for _, typ := range []domain.EventType{
		domain.EventStateChanged, domain.EventAgentText, domain.EventToolUse,
		domain.EventToolResult, domain.EventDiff, domain.EventTestResult,
	} {
		if !hasEvent(evs, typ) {
			t.Errorf("missing %s event", typ)
		}
	}
}

func TestQuestionThenFollowUpResumesSession(t *testing.T) {
	f := newFixture(t, "")
	id := f.newTask("FAKE:question Which file name should I use?", 0)
	rec := &recorder{Adapter: adapters.NewFake()}

	if err := Run(f.ctx, f.opts(id, rec)); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	task := f.expect(id, domain.StateWaitingInput)
	if task.PendingQuestion != "Which file name should I use?" {
		t.Errorf("pending_question = %q", task.PendingQuestion)
	}
	if task.SessionID == "" {
		t.Fatalf("session_id not stored after the first turn")
	}
	if !hasEvent(f.events(id), domain.EventQuestion) {
		t.Errorf("missing question event")
	}

	// The answer arrives through the API (CORE-2): store next_input, requeue, and claim again.
	if _, err := f.st.MutateTask(f.ctx, id, func(t *domain.Task) error {
		t.NextInput = "FAKE:write answer.txt from the answer"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.transition(id, domain.StateQueued)
	f.transition(id, domain.StatePreparing)

	if err := Run(f.ctx, f.opts(id, rec)); err != nil {
		t.Fatalf("follow-up Run: %v", err)
	}
	task = f.expect(id, domain.StateReviewReady)
	if task.Turns != 2 {
		t.Errorf("turns = %d, want 2", task.Turns)
	}
	if task.NextInput != "" || task.PendingQuestion != "" {
		t.Errorf("next_input=%q pending_question=%q, want both cleared", task.NextInput, task.PendingQuestion)
	}
	if _, err := os.Stat(filepath.Join(task.WorktreePath, "answer.txt")); err != nil {
		t.Errorf("follow-up answer was not applied: %v", err)
	}
	if len(rec.sessions) != 2 {
		t.Fatalf("CreateSession calls = %d, want 2", len(rec.sessions))
	}
	if rec.sessions[0].ResumeID != "" {
		t.Errorf("first turn resume id = %q, want empty", rec.sessions[0].ResumeID)
	}
	if rec.sessions[1].ResumeID != task.SessionID {
		t.Errorf("follow-up resume id = %q, want %q", rec.sessions[1].ResumeID, task.SessionID)
	}
}

func TestDenialMovesToWaitingApproval(t *testing.T) {
	f := newFixture(t, "")
	id := f.newTask("FAKE:deny Bash", 0)

	if err := Run(f.ctx, f.opts(id, adapters.NewFake())); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := f.expect(id, domain.StateWaitingApproval)

	var denials []struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal(task.PendingApproval, &denials); err != nil {
		t.Fatalf("pending_approval %q: %v", task.PendingApproval, err)
	}
	if len(denials) != 1 || denials[0].Tool != "Bash" {
		t.Errorf("pending_approval denials = %+v, want one Bash denial", denials)
	}
	if !hasEvent(f.events(id), domain.EventPermissionRequest) {
		t.Errorf("missing permission_request event")
	}
}

func TestAgentErrorRetriesThenFails(t *testing.T) {
	cases := []struct {
		name        string
		maxAttempts int
		want        domain.TaskState
	}{
		{"retries while attempts remain", 2, domain.StateRetrying},
		{"fails once attempts are exhausted", 1, domain.StateFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "")
			id := f.newTask("FAKE:fail", tc.maxAttempts)

			if err := Run(f.ctx, f.opts(id, adapters.NewFake())); err != nil {
				t.Fatalf("Run: %v", err)
			}
			task := f.expect(id, tc.want)
			if task.FailureReason != ReasonAgentError {
				t.Errorf("failure_reason = %q, want %q", task.FailureReason, ReasonAgentError)
			}
			if task.Attempt != 1 {
				t.Errorf("attempt = %d, want 1", task.Attempt)
			}
			if !strings.Contains(task.Error, "fake failure requested") {
				t.Errorf("error = %q, want the agent message", task.Error)
			}
		})
	}
}

func TestTestsFailedMovesToFailed(t *testing.T) {
	f := newFixture(t, "echo boom; exit 3")
	id := f.newTask("FAKE:write a.txt x", 0)

	if err := Run(f.ctx, f.opts(id, adapters.NewFake())); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := f.expect(id, domain.StateFailed)
	if task.FailureReason != ReasonTestsFailed {
		t.Errorf("failure_reason = %q, want %q", task.FailureReason, ReasonTestsFailed)
	}
	if task.TestStatus != "failed" {
		t.Errorf("test_status = %q, want failed", task.TestStatus)
	}
	if !strings.Contains(task.TestOutput, "boom") {
		t.Errorf("test_output = %q, want the test output", task.TestOutput)
	}
	if task.HeadCommit == "" {
		t.Errorf("head_commit not recorded for a failed validation")
	}
	if _, err := os.Stat(task.WorktreePath); err != nil {
		t.Errorf("worktree should be kept for inspection: %v", err)
	}
	if !hasEvent(f.events(id), domain.EventTestResult) {
		t.Errorf("missing test_result event")
	}
}

func TestCancelStopsTurn(t *testing.T) {
	f := newFixture(t, "")
	id := f.newTask("FAKE:sleep 10", 0)

	errc := make(chan error, 1)
	go func() { errc <- Run(f.ctx, f.opts(id, adapters.NewFake())) }()
	f.waitState(id, domain.StateRunning, 5*time.Second)

	// The API cancels the task while the agent is still running.
	if _, err := f.st.TransitionTask(f.ctx, id, []domain.TaskState{domain.StateRunning}, domain.StateCancelled, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after the task was cancelled")
	}
	f.expect(id, domain.StateCancelled)
}

func TestTimeoutFailsTurn(t *testing.T) {
	f := newFixture(t, "")
	id := f.newTask("FAKE:sleep 5", 0)
	opts := f.opts(id, adapters.NewFake())
	opts.Timeout = 300 * time.Millisecond

	if err := Run(f.ctx, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := f.expect(id, domain.StateFailed)
	if task.FailureReason != ReasonTimeout {
		t.Errorf("failure_reason = %q, want %q", task.FailureReason, ReasonTimeout)
	}
}

func TestEnvironmentIsScrubbed(t *testing.T) {
	// The test command only passes when the secrets are absent from its environment and PATH is present.
	f := newFixture(t, `test -z "$GH_TOKEN" && test -z "$AWS_SECRET_ACCESS_KEY" && test -n "$PATH"`)
	id := f.newTask("FAKE:write scrub.txt ok", 0)
	rec := &recorder{Adapter: adapters.NewFake()}
	opts := f.opts(id, rec)
	opts.Environ = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/tmp/home",
		"GH_TOKEN=ghp_example",
		"AWS_SECRET_ACCESS_KEY=example",
		"SOME_TOKEN=example",
		"LANG=C",
	}

	if err := Run(f.ctx, opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	task := f.expect(id, domain.StateReviewReady)
	if task.TestStatus != "passed" {
		t.Errorf("test_status = %q, want passed (secrets leaked into the test environment?)", task.TestStatus)
	}
	if len(rec.sessions) != 1 {
		t.Fatalf("CreateSession calls = %d, want 1", len(rec.sessions))
	}
	sess := rec.sessions[0]
	for _, kv := range sess.Env {
		for _, banned := range []string{"GH_TOKEN=", "AWS_SECRET_ACCESS_KEY=", "SOME_TOKEN="} {
			if strings.HasPrefix(kv, banned) {
				t.Errorf("agent environment contains %s", banned)
			}
		}
	}
	if !envHas(sess.Env, "PATH=") {
		t.Errorf("agent environment lost PATH")
	}
	if !containsString(sess.DeniedPaths, f.paths.SecretsDir()) {
		t.Errorf("denied paths = %v, want the secrets dir %s", sess.DeniedPaths, f.paths.SecretsDir())
	}
}

func envHas(env []string, prefix string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
