package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
)

const testStaleAfter = 30 * time.Second

// clock is a manually advanced time source shared by the scheduler and the store's heartbeat writes.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// fakeOS replaces worker processes: spawn hands out pids, alive and terminate track them.
type fakeOS struct {
	mu         sync.Mutex
	nextPID    int
	alive      map[int]bool
	spawned    []string
	terminated []int
	spawnErr   error
}

func newFakeOS() *fakeOS {
	return &fakeOS{nextPID: 1000, alive: map[int]bool{}}
}

func (f *fakeOS) spawn(_ context.Context, task domain.Task) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.spawnErr != nil {
		return 0, f.spawnErr
	}
	f.nextPID++
	f.alive[f.nextPID] = true
	f.spawned = append(f.spawned, task.ID)
	return f.nextPID, nil
}

func (f *fakeOS) isAlive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[pid]
}

func (f *fakeOS) kill(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, pid)
	delete(f.alive, pid)
	return nil
}

// setDead makes a worker disappear without going through terminate (a crash).
func (f *fakeOS) setDead(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.alive, pid)
}

func (f *fakeOS) spawnCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.spawned)
}

func (f *fakeOS) terminatedPIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.terminated)
}

type harness struct {
	t         *testing.T
	root      string
	st        *store.Store
	clk       *clock
	os        *fakeOS
	sched     *Scheduler
	projectID string
}

func newHarness(t *testing.T, lim Limits) *harness {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "rhymehub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	p, err := st.CreateProject(ctx, domain.Project{
		Name: "demo", RepoPath: root, DefaultBranch: "main",
		MaxConcurrent: 5, TimeoutMinutes: 30,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	h := &harness{
		t:         t,
		root:      root,
		st:        st,
		clk:       &clock{t: time.Now()},
		os:        newFakeOS(),
		projectID: p.ID,
	}
	h.sched = h.newScheduler(lim)
	return h
}

// newScheduler builds a scheduler over the same store and fakes. A second call simulates a restart.
func (h *harness) newScheduler(lim Limits) *Scheduler {
	h.t.Helper()
	s, err := New(Options{
		Store:      h.st,
		Paths:      config.NewPaths(h.root),
		Limits:     lim,
		Now:        h.clk.now,
		Spawn:      h.os.spawn,
		Alive:      h.os.isAlive,
		Terminate:  h.os.kill,
		StaleAfter: testStaleAfter,
		Tick:       time.Millisecond,
	})
	if err != nil {
		h.t.Fatalf("new scheduler: %v", err)
	}
	return s
}

func (h *harness) queue(title string) domain.Task {
	h.t.Helper()
	ctx := context.Background()
	created, err := h.st.CreateTask(ctx, domain.Task{
		ProjectID: h.projectID, Title: title, Prompt: "do it", Agent: "claude", State: domain.StateCreated,
	})
	if err != nil {
		h.t.Fatalf("create task: %v", err)
	}
	queued, err := h.st.TransitionTask(ctx, created.ID, []domain.TaskState{domain.StateCreated}, domain.StateQueued, nil)
	if err != nil {
		h.t.Fatalf("queue task: %v", err)
	}
	return queued
}

func (h *harness) get(id string) domain.Task {
	h.t.Helper()
	t, err := h.st.GetTask(context.Background(), id)
	if err != nil {
		h.t.Fatalf("get task %s: %v", id, err)
	}
	return t
}

func (h *harness) step() {
	h.t.Helper()
	if err := h.sched.Step(context.Background()); err != nil {
		h.t.Fatalf("step: %v", err)
	}
}

func TestClaimRespectsGlobalLimit(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 5})
	a := h.queue("a")
	b := h.queue("b")
	c := h.queue("c")

	h.step()

	if n := h.os.spawnCount(); n != 2 {
		t.Fatalf("spawned %d workers, want 2", n)
	}
	for _, id := range []string{a.ID, b.ID} {
		if got := h.get(id).State; got != domain.StatePreparing {
			t.Fatalf("task %s state = %s, want PREPARING", id, got)
		}
	}
	if got := h.get(c.ID).State; got != domain.StateQueued {
		t.Fatalf("third task state = %s, want QUEUED while the global limit is full", got)
	}
}

func TestSpawnRecordsHeartbeatPID(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 2})
	q := h.queue("x")

	h.step()

	got := h.get(q.ID)
	if got.State != domain.StatePreparing {
		t.Fatalf("state = %s, want PREPARING", got.State)
	}
	if got.WorkerPID != 1001 {
		t.Fatalf("worker pid = %d, want 1001", got.WorkerPID)
	}
	if got.HeartbeatAt == nil {
		t.Fatal("heartbeat not recorded after spawn")
	}
	if n := h.sched.ActiveWorkers(); n != 1 {
		t.Fatalf("ActiveWorkers = %d, want 1", n)
	}
}

func TestStaleHeartbeatRequeuesThenFails(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 2})
	q := h.queue("x")

	h.step() // pid 1001, attempt 0
	h.os.setDead(1001)
	h.clk.advance(testStaleAfter + time.Second)
	h.step() // stale, attempt 1 < 2: requeued and claimed again with pid 1002

	got := h.get(q.ID)
	if got.Attempt != 1 {
		t.Fatalf("attempt = %d after first loss, want 1", got.Attempt)
	}
	if got.State != domain.StatePreparing || got.WorkerPID != 1002 {
		t.Fatalf("after retry state=%s pid=%d, want PREPARING pid 1002", got.State, got.WorkerPID)
	}
	if n := h.os.spawnCount(); n != 2 {
		t.Fatalf("spawned %d workers, want 2", n)
	}

	h.os.setDead(1002)
	h.clk.advance(testStaleAfter + time.Second)
	h.step() // stale, attempt 2 is not < 2: fail

	got = h.get(q.ID)
	if got.State != domain.StateFailed {
		t.Fatalf("state = %s, want FAILED after exhausting attempts", got.State)
	}
	if got.FailureReason != codeWorkerLost {
		t.Fatalf("failure reason = %q, want %q", got.FailureReason, codeWorkerLost)
	}
	if got.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", got.Attempt)
	}
	if n := h.sched.ActiveWorkers(); n != 0 {
		t.Fatalf("ActiveWorkers = %d, want 0", n)
	}
}

func TestStaleLiveWorkerIsTerminated(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 2})
	h.queue("x")

	h.step() // pid 1001 stays alive
	h.clk.advance(testStaleAfter + time.Second)
	h.step()

	if !slices.Contains(h.os.terminatedPIDs(), 1001) {
		t.Fatalf("terminated = %v, want it to include the stale worker 1001", h.os.terminatedPIDs())
	}
}

func TestTimeoutFailsWithReason(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 2})
	ctx := context.Background()
	p, err := h.st.GetProject(ctx, h.projectID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	p.TimeoutMinutes = 1
	if _, err := h.st.UpdateProject(ctx, p); err != nil {
		t.Fatalf("update project: %v", err)
	}
	q := h.queue("x")

	h.step()
	h.clk.advance(2 * time.Minute)
	h.step()

	got := h.get(q.ID)
	if got.State != domain.StateFailed {
		t.Fatalf("state = %s, want FAILED on timeout", got.State)
	}
	if got.FailureReason != codeTimeout {
		t.Fatalf("failure reason = %q, want %q", got.FailureReason, codeTimeout)
	}
	if !slices.Contains(h.os.terminatedPIDs(), 1001) {
		t.Fatalf("terminated = %v, want the timed-out worker 1001", h.os.terminatedPIDs())
	}
}

func TestStartupReconciliation(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 2})
	dead := h.queue("dead")
	alive := h.queue("alive")

	h.step() // dead gets pid 1001, alive gets pid 1002
	h.os.setDead(1001)
	h.clk.advance(time.Second)

	restarted := h.newScheduler(Limits{Global: 2, DefaultPerAgent: 2})
	if err := restarted.reconcileStartup(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	d := h.get(dead.ID)
	if d.State != domain.StateQueued {
		t.Fatalf("dead worker's task state = %s, want QUEUED for retry", d.State)
	}
	if d.FailureReason != codeWorkerLost {
		t.Fatalf("dead worker's failure reason = %q, want %q", d.FailureReason, codeWorkerLost)
	}
	a := h.get(alive.ID)
	if a.State != domain.StatePreparing || a.WorkerPID != 1002 {
		t.Fatalf("live worker's task state=%s pid=%d, want PREPARING pid 1002 untouched", a.State, a.WorkerPID)
	}
}

func TestDrainStopsClaims(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 2})
	q := h.queue("x")

	h.sched.Drain(true)
	if !h.sched.Draining() {
		t.Fatal("Draining() = false after Drain(true)")
	}
	h.step()
	if n := h.os.spawnCount(); n != 0 {
		t.Fatalf("spawned %d workers while draining, want 0", n)
	}
	if got := h.get(q.ID).State; got != domain.StateQueued {
		t.Fatalf("state = %s while draining, want QUEUED", got)
	}

	h.sched.Drain(false)
	h.step()
	if n := h.os.spawnCount(); n != 1 {
		t.Fatalf("spawned %d workers after resume, want 1", n)
	}
}

func TestSetLimitsTakesEffect(t *testing.T) {
	h := newHarness(t, Limits{Global: 1, DefaultPerAgent: 2})
	h.queue("a")
	h.queue("b")

	h.step()
	if n := h.os.spawnCount(); n != 1 {
		t.Fatalf("spawned %d workers under Global 1, want 1", n)
	}

	h.sched.SetLimits(Limits{Global: 2, DefaultPerAgent: 2})
	if got := h.sched.CurrentLimits().Global; got != 2 {
		t.Fatalf("CurrentLimits().Global = %d, want 2", got)
	}
	h.step()
	if n := h.os.spawnCount(); n != 2 {
		t.Fatalf("spawned %d workers after raising limit, want 2", n)
	}
}

func TestSpawnFailureRetriesThenFails(t *testing.T) {
	h := newHarness(t, Limits{Global: 2, DefaultPerAgent: 2})
	h.os.spawnErr = errors.New("exec failed")
	q := h.queue("x")

	h.step()

	got := h.get(q.ID)
	if got.State != domain.StateFailed {
		t.Fatalf("state = %s, want FAILED after two failed spawns", got.State)
	}
	if got.FailureReason != codeSpawnFailed {
		t.Fatalf("failure reason = %q, want %q", got.FailureReason, codeSpawnFailed)
	}
	if got.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", got.Attempt)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("New without store: err = %v, want ErrInvalid", err)
	}
	if _, err := New(Options{Store: (*store.Store)(nil)}); err == nil {
		t.Fatal("New without paths or Spawn: want error, got nil")
	}
}

func TestLimitsNormalize(t *testing.T) {
	got := normalizeLimits(Limits{Global: 0, DefaultPerAgent: -1, PerAgent: map[string]int{"codex": 3, "claude": 0, "": 4}})
	if got.Global != DefaultGlobal || got.DefaultPerAgent != DefaultPerAgent {
		t.Fatalf("defaults = %+v, want global %d and per-agent %d", got, DefaultGlobal, DefaultPerAgent)
	}
	if got.PerAgent["codex"] != 3 || len(got.PerAgent) != 1 {
		t.Fatalf("PerAgent = %v, want only codex:3", got.PerAgent)
	}
}

func TestWorkerEnvScrubsSecrets(t *testing.T) {
	root := "/tmp/rhymehub-test-root"
	env := workerEnv(config.NewPaths(root), []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/tester",
		"ANTHROPIC_API_KEY=sk-secret",
		"GITHUB_TOKEN=ghp-secret",
		"RHYMEHUB_HOME=/elsewhere",
	})
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") || strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			t.Fatalf("worker env leaked a secret: %q", kv)
		}
	}
	if !slices.Contains(env, "PATH=/usr/bin:/bin") {
		t.Fatalf("worker env dropped PATH: %v", env)
	}
	if !slices.Contains(env, envRhymeHome+"="+root) {
		t.Fatalf("worker env RHYMEHUB_HOME = %v, want %s", env, root)
	}
	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, envRhymeHome+"=") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("RHYMEHUB_HOME appears %d times, want 1", count)
	}
}
