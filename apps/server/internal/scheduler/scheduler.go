// Package scheduler claims QUEUED tasks under concurrency limits, starts a worker process for each claimed task,
// watches worker heartbeats, enforces project timeouts, and recovers tasks whose worker is gone.
//
// Worker processes are started detached (their own session) with a scrubbed environment. The scheduler never
// reads secrets and never passes credentials to workers.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/redact"
	"github.com/lafin716/rhymehub/internal/store"
)

// Defaults.
const (
	DefaultTick         = time.Second
	DefaultStaleAfter   = 30 * time.Second
	DefaultTimeout      = 30 * time.Minute
	DefaultGlobal       = 2
	DefaultPerAgent     = 2
	maxClaimsPerPass    = 16
	codeWorkerLost      = "worker_lost"
	codeSpawnFailed     = "spawn_failed"
	codeTimeout         = "timeout"
	workerSubcommand    = "worker"
	workerTaskFlag      = "--task"
	envRhymeHome        = config.EnvHome
	spawnFailedDetail   = "worker could not be started"
	workerLostDetailFmt = "worker heartbeat is older than %s"
)

// Store is the persistence surface the scheduler uses. *store.Store satisfies it.
type Store interface {
	ClaimNextQueued(ctx context.Context, lim store.ClaimLimits) (domain.Task, bool, error)
	GetTask(ctx context.Context, id string) (domain.Task, error)
	GetProject(ctx context.Context, id string) (domain.Project, error)
	UpdateHeartbeat(ctx context.Context, id string, pid int, at time.Time) error
	ListOrphans(ctx context.Context, staleBefore time.Time) ([]domain.Task, error)
	TransitionTask(ctx context.Context, id string, expectFrom []domain.TaskState, to domain.TaskState, ev *store.EventInput) (domain.Task, error)
	MutateTask(ctx context.Context, id string, fn func(t *domain.Task) error) (domain.Task, error)
}

// Limits are the concurrency limits applied when claiming tasks. Global and DefaultPerAgent values <= 0 fall back to
// the defaults (2 and 2); the scheduler never runs unlimited. PerAgent entries <= 0 are dropped, so those agents use
// DefaultPerAgent. Per-project limits come from projects.max_concurrent.
type Limits struct {
	Global          int
	DefaultPerAgent int
	PerAgent        map[string]int
}

// LimitsFromConfig converts the server configuration into scheduler limits.
func LimitsFromConfig(c config.SchedulerConfig) Limits {
	return Limits{Global: c.GlobalMaxRunning, DefaultPerAgent: c.DefaultPerAgent, PerAgent: c.PerAgent}
}

// SpawnFunc starts a worker for task and returns the worker's process id.
type SpawnFunc func(ctx context.Context, task domain.Task) (int, error)

// Options configures a Scheduler.
type Options struct {
	Store Store
	// Paths supplies RHYMEHUB_HOME for the default Spawn. Required when Spawn is nil.
	Paths  config.Paths
	Limits Limits
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// Tick is the Run interval. Defaults to 1 s.
	Tick time.Duration
	// StaleAfter is how old a heartbeat may be before the worker is treated as lost. Defaults to 30 s.
	StaleAfter time.Duration
	// Spawn starts a worker. Defaults to a detached "rhymehub worker --task <id>" process.
	Spawn SpawnFunc
	// Alive reports whether a process exists. Defaults to a signal-0 probe.
	Alive func(pid int) bool
	// Terminate asks a process to stop (SIGTERM by default).
	Terminate func(pid int) error
	// Logger receives scheduler diagnostics. Defaults to slog.Default().
	Logger *slog.Logger
}

type worker struct {
	taskID string
	pid    int
}

// Scheduler owns task claiming and worker supervision.
type Scheduler struct {
	store     Store
	paths     config.Paths
	now       func() time.Time
	tick      time.Duration
	stale     time.Duration
	spawn     SpawnFunc
	alive     func(int) bool
	terminate func(int) error
	log       *slog.Logger

	mu       sync.Mutex
	limits   Limits
	draining bool
	active   map[string]int // task id -> worker pid, for workers this process started
}

// New validates opts and returns a Scheduler. It does not touch tasks until Run or Step is called.
func New(opts Options) (*Scheduler, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("%w: scheduler store is nil", domain.ErrInvalid)
	}
	s := &Scheduler{
		store:     opts.Store,
		paths:     opts.Paths,
		now:       opts.Now,
		tick:      opts.Tick,
		stale:     opts.StaleAfter,
		spawn:     opts.Spawn,
		alive:     opts.Alive,
		terminate: opts.Terminate,
		log:       opts.Logger,
		limits:    normalizeLimits(opts.Limits),
		active:    map[string]int{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.tick <= 0 {
		s.tick = DefaultTick
	}
	if s.stale <= 0 {
		s.stale = DefaultStaleAfter
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.spawn == nil {
		if opts.Paths.Root == "" {
			return nil, fmt.Errorf("%w: scheduler paths root is empty and no Spawn is set", domain.ErrInvalid)
		}
		s.spawn = defaultSpawn(opts.Paths)
	}
	if s.alive == nil {
		s.alive = processAlive
	}
	if s.terminate == nil {
		s.terminate = terminateProcess
	}
	return s, nil
}

// Run performs startup reconciliation once, then calls Step every tick until ctx is done.
func (s *Scheduler) Run(ctx context.Context) error {
	if err := s.reconcileStartup(ctx); err != nil && ctx.Err() == nil {
		s.log.Error("scheduler startup reconciliation failed", "err", err)
	}
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		if err := s.Step(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("scheduler pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Step runs one supervision and dispatch pass: timeouts, stale heartbeats, pruning finished workers, then claiming.
// Run calls it on every tick; tests call it directly.
func (s *Scheduler) Step(ctx context.Context) error {
	var errs []error
	if err := s.checkTimeouts(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := s.checkStale(ctx); err != nil {
		errs = append(errs, err)
	}
	s.prune(ctx)
	if err := s.dispatch(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Drain stops (true) or resumes (false) claiming new tasks. Running workers are not affected.
func (s *Scheduler) Drain(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.draining = on
}

// Draining reports whether new claims are paused.
func (s *Scheduler) Draining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// ActiveWorkers returns how many workers started by this process are still tracked as running.
func (s *Scheduler) ActiveWorkers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.active)
}

// SetLimits replaces the concurrency limits. It takes effect on the next claim (PATCH /agents/limits).
func (s *Scheduler) SetLimits(l Limits) {
	n := normalizeLimits(l)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limits = n
}

// CurrentLimits returns a copy of the effective limits.
func (s *Scheduler) CurrentLimits() Limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyLimits(s.limits)
}

func normalizeLimits(l Limits) Limits {
	out := Limits{Global: l.Global, DefaultPerAgent: l.DefaultPerAgent, PerAgent: map[string]int{}}
	if out.Global <= 0 {
		out.Global = DefaultGlobal
	}
	if out.DefaultPerAgent <= 0 {
		out.DefaultPerAgent = DefaultPerAgent
	}
	for agent, v := range l.PerAgent {
		if agent != "" && v > 0 {
			out.PerAgent[agent] = v
		}
	}
	return out
}

func copyLimits(l Limits) Limits {
	out := Limits{Global: l.Global, DefaultPerAgent: l.DefaultPerAgent, PerAgent: make(map[string]int, len(l.PerAgent))}
	for k, v := range l.PerAgent {
		out.PerAgent[k] = v
	}
	return out
}

func (s *Scheduler) claimLimits() store.ClaimLimits {
	l := s.CurrentLimits()
	return store.ClaimLimits{Global: l.Global, DefaultPerAgent: l.DefaultPerAgent, PerAgent: l.PerAgent}
}

// reconcileStartup recovers PREPARING and RUNNING tasks whose recorded worker process no longer exists. Tasks with a
// live worker are left alone; the stale-heartbeat check decides on those later.
func (s *Scheduler) reconcileStartup(ctx context.Context) error {
	orphans, err := s.store.ListOrphans(ctx, s.now())
	if err != nil {
		return fmt.Errorf("scheduler: list orphans: %w", err)
	}
	var errs []error
	for _, t := range orphans {
		if t.State != domain.StatePreparing && t.State != domain.StateRunning {
			continue
		}
		if t.WorkerPID > 0 && s.alive(t.WorkerPID) {
			continue
		}
		if err := s.settle(ctx, t.ID, codeWorkerLost, "no live worker found at startup", true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// checkStale recovers running tasks whose heartbeat is older than StaleAfter. A worker that is still alive is
// terminated first so it cannot keep writing after its task has been requeued.
func (s *Scheduler) checkStale(ctx context.Context) error {
	orphans, err := s.store.ListOrphans(ctx, s.now().Add(-s.stale))
	if err != nil {
		return fmt.Errorf("scheduler: list stale tasks: %w", err)
	}
	var errs []error
	for _, t := range orphans {
		s.stopWorker(t.WorkerPID)
		detail := fmt.Sprintf(workerLostDetailFmt, s.stale)
		if err := s.settle(ctx, t.ID, codeWorkerLost, detail, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// checkTimeouts fails tracked tasks that have run longer than their project's TimeoutMinutes (default 30).
func (s *Scheduler) checkTimeouts(ctx context.Context) error {
	now := s.now()
	var errs []error
	for _, w := range s.snapshot() {
		t, err := s.store.GetTask(ctx, w.taskID)
		if err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				s.forget(w.taskID)
				continue
			}
			errs = append(errs, err)
			continue
		}
		if !domain.IsRunning(t.State) || t.StartedAt == nil {
			continue
		}
		p, err := s.store.GetProject(ctx, t.ProjectID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		limit := timeoutFor(p)
		if now.Before(t.StartedAt.Add(limit)) {
			continue
		}
		s.stopWorker(w.pid)
		detail := fmt.Sprintf("task exceeded the %d minute timeout", int(limit/time.Minute))
		if err := s.settle(ctx, t.ID, codeTimeout, detail, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// prune forgets tracked workers whose task has left the running states.
func (s *Scheduler) prune(ctx context.Context) {
	for _, w := range s.snapshot() {
		t, err := s.store.GetTask(ctx, w.taskID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && !domain.IsRunning(t.State)) {
			s.forget(w.taskID)
		}
	}
}

func (s *Scheduler) dispatch(ctx context.Context) error {
	if s.Draining() {
		return nil
	}
	for i := 0; i < maxClaimsPerPass; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		task, ok, err := s.store.ClaimNextQueued(ctx, s.claimLimits())
		if err != nil {
			return fmt.Errorf("scheduler: claim: %w", err)
		}
		if !ok {
			return nil
		}
		s.start(ctx, task)
	}
	return nil
}

// start spawns a worker for a claimed task and records its pid as the first heartbeat.
func (s *Scheduler) start(ctx context.Context, task domain.Task) {
	pid, err := s.spawn(ctx, task)
	if err != nil {
		s.log.Error("scheduler: spawn worker failed", "task", task.ID, "err", err)
		if serr := s.settle(ctx, task.ID, codeSpawnFailed, spawnFailedDetail, true); serr != nil {
			s.log.Error("scheduler: settle after spawn failure", "task", task.ID, "err", serr)
		}
		return
	}
	if err := s.store.UpdateHeartbeat(ctx, task.ID, pid, s.now()); err != nil {
		// The task left the running states while the worker was starting. The worker is no longer wanted.
		s.log.Error("scheduler: record worker heartbeat", "task", task.ID, "err", err)
		s.stopWorker(pid)
		return
	}
	s.mu.Lock()
	s.active[task.ID] = pid
	s.mu.Unlock()
}

// settle ends a worker's run. A retryable failure requeues the task through RETRYING while attempts remain
// (Attempt+1 < MaxAttempts); otherwise the task moves to FAILED with code as its failure reason.
func (s *Scheduler) settle(ctx context.Context, id, code, detail string, retryable bool) error {
	cur, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			s.forget(id)
			return nil
		}
		return err
	}
	from := cur.State
	if !domain.IsRunning(from) {
		s.forget(id)
		return nil
	}
	attempt := cur.Attempt + 1
	retry := retryable && attempt < cur.MaxAttempts
	if _, err := s.store.MutateTask(ctx, id, func(t *domain.Task) error {
		t.Attempt = attempt
		t.WorkerPID = 0
		t.HeartbeatAt = nil
		t.FailureReason = code
		t.Error = detail
		return nil
	}); err != nil {
		return fmt.Errorf("scheduler: record failure for %s: %w", id, err)
	}
	if retry {
		if _, err := s.store.TransitionTask(ctx, id, []domain.TaskState{from}, domain.StateRetrying,
			&store.EventInput{Reason: code}); err != nil {
			return fmt.Errorf("scheduler: retry %s: %w", id, err)
		}
		if _, err := s.store.TransitionTask(ctx, id, []domain.TaskState{domain.StateRetrying}, domain.StateQueued,
			&store.EventInput{Reason: "requeued after " + code}); err != nil {
			return fmt.Errorf("scheduler: requeue %s: %w", id, err)
		}
	} else {
		if _, err := s.store.TransitionTask(ctx, id, []domain.TaskState{from}, domain.StateFailed,
			&store.EventInput{Reason: code}); err != nil {
			return fmt.Errorf("scheduler: fail %s: %w", id, err)
		}
	}
	s.forget(id)
	return nil
}

// CancelTask stops the worker of a task that the API already moved to CANCELLED. It uses the pid tracked by
// this process, falling back to the pid recorded on the task row. It never changes task state.
func (s *Scheduler) CancelTask(ctx context.Context, taskID string) error {
	s.mu.Lock()
	pid, ok := s.active[taskID]
	s.mu.Unlock()
	if !ok {
		t, err := s.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		pid = t.WorkerPID
	}
	s.stopWorker(pid)
	s.forget(taskID)
	return nil
}

func (s *Scheduler) stopWorker(pid int) {
	if pid <= 0 || !s.alive(pid) {
		return
	}
	if err := s.terminate(pid); err != nil {
		s.log.Error("scheduler: terminate worker", "pid", pid, "err", err)
	}
}

func (s *Scheduler) snapshot() []worker {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]worker, 0, len(s.active))
	for id, pid := range s.active {
		out = append(out, worker{taskID: id, pid: pid})
	}
	return out
}

func (s *Scheduler) forget(taskID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, taskID)
}

func timeoutFor(p domain.Project) time.Duration {
	if p.TimeoutMinutes <= 0 {
		return DefaultTimeout
	}
	return time.Duration(p.TimeoutMinutes) * time.Minute
}

// defaultSpawn starts "<rhymehub> worker --task <id>" in its own session with a scrubbed environment.
func defaultSpawn(paths config.Paths) SpawnFunc {
	return func(_ context.Context, task domain.Task) (int, error) {
		exe, err := os.Executable()
		if err != nil {
			return 0, fmt.Errorf("scheduler: locate executable: %w", err)
		}
		devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return 0, fmt.Errorf("scheduler: open %s: %w", os.DevNull, err)
		}
		cmd := exec.Command(exe, workerSubcommand, workerTaskFlag, task.ID)
		cmd.Env = workerEnv(paths, os.Environ())
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
		if err := cmd.Start(); err != nil {
			devNull.Close()
			return 0, fmt.Errorf("scheduler: start worker: %w", err)
		}
		pid := cmd.Process.Pid
		// Reap the worker when it exits so it does not linger as a zombie while the server runs.
		go func() {
			_ = cmd.Wait()
			devNull.Close()
		}()
		return pid, nil
	}
}

// workerEnv keeps only allowlisted variables from environ (denylisted names never pass) and sets RHYMEHUB_HOME.
func workerEnv(paths config.Paths, environ []string) []string {
	env := redact.ScrubEnv(environ, nil)
	return append(env, envRhymeHome+"="+paths.Root)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminateProcess(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
