// Package worker runs one agent task end to end: workspace preparation, prompt building, streaming the
// adapter's events into task_events, the end-of-turn decision (WAITING_INPUT, WAITING_APPROVAL, VALIDATING),
// validation (commit, diff, test command), cancellation and timeouts. See docs/AGENT_RUNTIME.md.
//
// Run executes exactly one turn. Follow-up turns are started by a later Run after the state has moved back
// to PREPARING through the scheduler (CORE-2).
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	adapters "github.com/lafin716/rhymehub/packages/agent-adapters"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/redact"
	"github.com/lafin716/rhymehub/internal/store"
	"github.com/lafin716/rhymehub/internal/workspace"
)

// Failure reasons stored in tasks.failure_reason and in the state_changed event reason
// (docs/AGENT_RUNTIME.md section 5).
const (
	ReasonAgentUnavailable  = "agent_unavailable"
	ReasonAgentError        = "agent_error"
	ReasonTimeout           = "timeout"
	ReasonTestsFailed       = "tests_failed"
	ReasonWorkerInterrupted = "worker_interrupted"
	ReasonWorkspaceError    = "workspace_error"
	ReasonSessionError      = "session_error"
)

const (
	defaultHeartbeat      = 5 * time.Second
	defaultTimeoutMinutes = 30
)

// errCancelled is returned internally when the task was cancelled while the worker ran. Run maps it to nil:
// CANCELLED is a normal terminal state, not a worker error.
var errCancelled = errors.New("worker: task cancelled")

// Options configures one worker run.
type Options struct {
	// Store is the rhymehub database. Required.
	Store *store.Store
	// Paths is the rhymehub home layout. Worktrees are created under Paths.WorktreesDir(). Required.
	Paths config.Paths
	// TaskID is the task to run. The task must be in PREPARING (the scheduler claims it). Required.
	TaskID string
	// Adapter overrides the adapter. When nil, adapters.Get(task.Agent) is used.
	Adapter adapters.Adapter
	// Environ is the parent environment. nil means os.Environ(). It is scrubbed before any agent or test process.
	Environ []string
	// Timeout overrides the per-turn timeout. When <= 0 the project's TimeoutMinutes is used (default 30).
	Timeout time.Duration
	// HeartbeatEvery is the heartbeat and cancel-check interval. <= 0 means 5 seconds.
	HeartbeatEvery time.Duration
	// PID is recorded as the worker pid in the heartbeat. 0 means os.Getpid().
	PID int
}

// Run executes one turn of a task and moves it to its next state. It returns nil when the task reached a
// state the worker is responsible for (including WAITING_INPUT, WAITING_APPROVAL, REVIEW_READY, FAILED,
// RETRYING or CANCELLED). It returns an error only for infrastructure failures that left the task in a
// running state (the caller or orphan recovery must handle those).
func Run(ctx context.Context, opts Options) error {
	if opts.Store == nil {
		return fmt.Errorf("%w: worker store is required", domain.ErrInvalid)
	}
	if opts.TaskID == "" {
		return fmt.Errorf("%w: worker task id is required", domain.ErrInvalid)
	}
	if opts.Paths.Root == "" {
		return fmt.Errorf("%w: worker paths are required", domain.ErrInvalid)
	}
	if opts.HeartbeatEvery <= 0 {
		opts.HeartbeatEvery = defaultHeartbeat
	}
	if opts.PID == 0 {
		opts.PID = os.Getpid()
	}
	if opts.Environ == nil {
		opts.Environ = os.Environ()
	}
	w := &runner{
		opts:    opts,
		st:      opts.Store,
		bg:      context.WithoutCancel(ctx),
		env:     redact.ScrubEnv(opts.Environ, nil),
		adapter: opts.Adapter,
	}
	err := w.execute(ctx)
	if errors.Is(err, errCancelled) {
		return nil
	}
	return err
}

type runner struct {
	opts    Options
	st      *store.Store
	bg      context.Context // used for store writes so they still land when ctx is cancelled
	env     []string        // scrubbed environment for agent and test processes
	adapter adapters.Adapter
	task    domain.Task
	project domain.Project
	timeout time.Duration
	session *adapters.Session
	outcome turnOutcome
	// storeErr is the first store failure while streaming. The turn is drained, then the error is returned.
	storeErr error
}

// turnOutcome collects what the adapter reported during one turn.
type turnOutcome struct {
	sessionID string
	question  string
	denials   []adapters.Denial
	errMsgs   []string
	doneText  string
	isError   bool
	sawDone   bool
}

func (w *runner) execute(ctx context.Context) error {
	t, err := w.st.GetTask(w.bg, w.opts.TaskID)
	if err != nil {
		return err
	}
	if t.State != domain.StatePreparing {
		return fmt.Errorf("%w: task %s is %s, expected PREPARING", domain.ErrStateConflict, t.ID, t.State)
	}
	w.task = t
	p, err := w.st.GetProject(w.bg, t.ProjectID)
	if err != nil {
		return err
	}
	w.project = p
	w.timeout = w.opts.Timeout
	if w.timeout <= 0 {
		minutes := p.TimeoutMinutes
		if minutes <= 0 {
			minutes = defaultTimeoutMinutes
		}
		w.timeout = time.Duration(minutes) * time.Minute
	}
	if w.adapter == nil {
		a, err := adapters.Get(t.Agent)
		if err != nil {
			return w.failTask(domain.StatePreparing, ReasonAgentUnavailable, err.Error())
		}
		w.adapter = a
	}
	det := w.adapter.Detect(ctx)
	if !det.Available {
		msg := det.Error
		if msg == "" {
			msg = "agent CLI is not available"
		}
		return w.failTask(domain.StatePreparing, ReasonAgentUnavailable, msg)
	}
	if err := w.prepare(ctx); err != nil {
		return w.failTask(domain.StatePreparing, ReasonWorkspaceError, err.Error())
	}
	return w.runTurn(ctx)
}

// prepare creates the task worktree on first use. A task that already has a worktree (follow-up or retry)
// reuses it.
func (w *runner) prepare(ctx context.Context) error {
	if w.task.WorktreePath != "" {
		if _, err := os.Stat(w.task.WorktreePath); err != nil {
			return fmt.Errorf("worktree %s is missing: %w", w.task.WorktreePath, err)
		}
		return nil
	}
	repo := w.project.RepoPath
	if err := workspace.ValidateRepo(ctx, repo); err != nil {
		return err
	}
	base := w.project.DefaultBranch
	if base == "" {
		b, err := workspace.DefaultBranch(ctx, repo)
		if err != nil {
			return err
		}
		base = b
	}
	wt := filepath.Join(w.opts.Paths.WorktreesDir(), w.task.ID)
	branch, sha, err := workspace.CreateWorktree(ctx, repo, wt, w.task.ID, base)
	if err != nil {
		return err
	}
	t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
		t.Branch = branch
		t.WorktreePath = wt
		t.BaseCommit = sha
		return nil
	})
	if err != nil {
		return err
	}
	w.task = t
	return nil
}

// runTurn starts the session, streams one turn, and applies the end-of-turn decision.
func (w *runner) runTurn(ctx context.Context) error {
	if err := w.move(domain.StatePreparing, domain.StateRunning, "turn started"); err != nil {
		return err
	}
	t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
		t.PendingQuestion = ""
		t.PendingApproval = nil
		t.Turns++
		t.WorkerPID = w.opts.PID
		return nil
	})
	if err != nil {
		return err
	}
	w.task = t

	// A follow-up is a task with a session and an answer waiting in next_input. Anything else starts fresh.
	resume := t.SessionID != "" && strings.TrimSpace(t.NextInput) != ""
	resumeID := ""
	if resume {
		resumeID = t.SessionID
	}
	model := t.Model
	if model == "" {
		model = w.project.DefaultModel
	}
	sess, err := w.adapter.CreateSession(ctx, adapters.SessionOptions{
		TaskID:       t.ID,
		WorkDir:      t.WorktreePath,
		Model:        model,
		ResumeID:     resumeID,
		AllowedTools: mergeTools(w.project.AllowedTools, t.AllowedTools),
		DeniedPaths:  []string{w.opts.Paths.SecretsDir()},
		Env:          w.env,
		Timeout:      w.timeout,
	})
	if err != nil {
		return w.retryOrFail(domain.StateRunning, ReasonSessionError, err.Error())
	}
	w.session = sess

	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var cancelled atomic.Bool
	wg.Add(1)
	taskID, sess := t.ID, w.session
	go func() {
		defer wg.Done()
		w.watch(turnCtx, taskID, sess, stop, &cancelled)
	}()

	ch, err := w.start(turnCtx, resume)
	if err != nil {
		close(stop)
		wg.Wait()
		return w.retryOrFail(domain.StateRunning, ReasonAgentError, err.Error())
	}
	for ev := range ch {
		if err := w.handle(ev); err != nil && w.storeErr == nil {
			w.storeErr = err
		}
	}
	close(stop)
	wg.Wait()
	return w.decide(ctx, &cancelled)
}

func (w *runner) start(ctx context.Context, resume bool) (<-chan adapters.Event, error) {
	if resume {
		return w.adapter.Respond(ctx, w.session, BuildFollowUp(w.project, w.task.NextInput))
	}
	return w.adapter.Execute(ctx, w.session, BuildPrompt(w.project, w.task))
}

// watch sends heartbeats and notices a cancel request. A task moved to CANCELLED by the API stops the agent.
// It takes the task id and session by value: the main goroutine keeps updating w.task while this runs.
func (w *runner) watch(ctx context.Context, taskID string, sess *adapters.Session, stop <-chan struct{}, cancelled *atomic.Bool) {
	beat := func() {
		t, err := w.st.GetTask(w.bg, taskID)
		if err == nil && t.State == domain.StateCancelled {
			// Cancel is repeated on every tick: the adapter may not have started its run yet.
			cancelled.Store(true)
			_ = w.adapter.Cancel(w.bg, sess)
			return
		}
		_ = w.st.UpdateHeartbeat(w.bg, taskID, w.opts.PID, time.Now())
	}
	beat()
	ticker := time.NewTicker(w.opts.HeartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			beat()
		}
	}
}

// handle persists one adapter event. The stream is always drained, even after a store error.
func (w *runner) handle(ev adapters.Event) error {
	switch ev.Type {
	case adapters.EventSession:
		if ev.SessionID == "" || ev.SessionID == w.outcome.sessionID {
			return nil
		}
		w.outcome.sessionID = ev.SessionID
		t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
			t.SessionID = ev.SessionID
			return nil
		})
		if err != nil {
			return err
		}
		w.task = t
		return nil
	case adapters.EventText:
		if q, ok := adapters.ParseQuestion(ev.Text); ok && w.outcome.question == "" {
			w.outcome.question = q
		}
		return w.emit(domain.EventAgentText, map[string]any{"text": redactText(ev.Text)})
	case adapters.EventToolUse:
		return w.emit(domain.EventToolUse, map[string]any{"name": ev.Tool, "input": inputPayload(ev.Input)})
	case adapters.EventToolResult:
		return w.emit(domain.EventToolResult, map[string]any{"ok": ev.OK, "output": redactText(ev.Text)})
	case adapters.EventError:
		msg := redactText(ev.Text)
		w.outcome.errMsgs = append(w.outcome.errMsgs, msg)
		return w.emit(domain.EventAgentError, map[string]any{"message": msg})
	case adapters.EventUsage:
		if ev.CostUSD != 0 {
			t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
				t.CostUSD += ev.CostUSD
				return nil
			})
			if err != nil {
				return err
			}
			w.task = t
		}
		return w.emit(domain.EventUsage, map[string]any{
			"cost_usd":      ev.CostUSD,
			"input_tokens":  ev.InputTokens,
			"output_tokens": ev.OutputTokens,
			"turns":         ev.Turns,
		})
	case adapters.EventPermissionDenied:
		w.outcome.denials = append(w.outcome.denials, ev.Denials...)
		return w.emit(domain.EventPermissionRequest, map[string]any{"denials": denialList(ev.Denials)})
	case adapters.EventDone:
		w.outcome.sawDone = true
		w.outcome.doneText = ev.Text
		w.outcome.isError = ev.IsError
		if q, ok := adapters.ParseQuestion(ev.Text); ok && w.outcome.question == "" {
			w.outcome.question = q
		}
	}
	return nil
}

// decide applies the end-of-turn rules. Order: cancel, worker interruption, timeout, agent error,
// permission denials (WAITING_APPROVAL), question (WAITING_INPUT), then validation.
func (w *runner) decide(ctx context.Context, cancelled *atomic.Bool) error {
	o := w.outcome
	if cancelled.Load() {
		return w.move(domain.StateRunning, domain.StateCancelled, "cancelled")
	}
	if w.storeErr != nil {
		return w.storeErr
	}
	// The adapter reports "cancelled" when the parent context ends too, so the interruption check comes first.
	if ctx.Err() != nil {
		return w.retryOrFail(domain.StateRunning, ReasonWorkerInterrupted, "worker interrupted during the agent turn")
	}
	if o.doneText == "cancelled" {
		return w.move(domain.StateRunning, domain.StateCancelled, "cancelled")
	}
	if o.doneText == "timeout" {
		return w.failTask(domain.StateRunning, ReasonTimeout, fmt.Sprintf("agent turn exceeded %s", w.timeout))
	}
	if !o.sawDone || o.isError {
		detail := strings.Join(o.errMsgs, "; ")
		if detail == "" {
			detail = redactText(o.doneText)
		}
		if detail == "" {
			detail = "agent reported an error"
		}
		return w.retryOrFail(domain.StateRunning, ReasonAgentError, detail)
	}
	if err := w.consumeInput(); err != nil {
		return err
	}
	if len(o.denials) > 0 {
		raw, err := json.Marshal(denialList(o.denials))
		if err != nil {
			return err
		}
		if _, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
			t.PendingApproval = json.RawMessage(raw)
			return nil
		}); err != nil {
			return err
		}
		return w.move(domain.StateRunning, domain.StateWaitingApproval, "permission_request")
	}
	if o.question != "" {
		q := redactText(o.question)
		if _, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
			t.PendingQuestion = q
			return nil
		}); err != nil {
			return err
		}
		if err := w.emit(domain.EventQuestion, map[string]any{"text": q}); err != nil {
			return err
		}
		return w.move(domain.StateRunning, domain.StateWaitingInput, "question")
	}
	if err := w.move(domain.StateRunning, domain.StateValidating, "turn completed"); err != nil {
		return err
	}
	return w.validate(ctx)
}

// consumeInput clears the answer that this turn has used. It is kept on agent errors so a retry resumes with it.
func (w *runner) consumeInput() error {
	if w.task.NextInput == "" {
		return nil
	}
	t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
		t.NextInput = ""
		return nil
	})
	if err != nil {
		return err
	}
	w.task = t
	return nil
}

// validate commits the worktree, records the diff, runs the project's test command, and moves the task to
// REVIEW_READY or FAILED (tests_failed). The worktree is kept in place for inspection in every outcome.
func (w *runner) validate(ctx context.Context) error {
	if ctx.Err() != nil {
		return w.retryOrFail(domain.StateValidating, ReasonWorkerInterrupted, "worker interrupted during validation")
	}
	wt := w.task.WorktreePath
	base := w.task.BaseCommit

	sha, changed, err := workspace.CommitAll(ctx, wt, commitMessage(w.task))
	if err != nil {
		return w.failTask(domain.StateValidating, ReasonWorkspaceError, err.Error())
	}
	head := sha
	if !changed {
		head, err = headCommit(ctx, wt)
		if err != nil {
			return w.failTask(domain.StateValidating, ReasonWorkspaceError, err.Error())
		}
	}
	diff, err := workspace.Diff(ctx, wt, base)
	if err != nil {
		return w.failTask(domain.StateValidating, ReasonWorkspaceError, err.Error())
	}
	files, err := changedFiles(ctx, wt, base)
	if err != nil {
		return w.failTask(domain.StateValidating, ReasonWorkspaceError, err.Error())
	}
	stat := redactText(diff.Stat)
	t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
		t.HeadCommit = head
		t.DiffStat = stat
		return nil
	})
	if err != nil {
		return err
	}
	w.task = t
	if err := w.emit(domain.EventDiff, map[string]any{"stat": stat, "files": files}); err != nil {
		return err
	}

	if strings.TrimSpace(w.project.TestCmd) == "" {
		if _, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
			t.TestStatus = "skipped"
			return nil
		}); err != nil {
			return err
		}
		if err := w.emit(domain.EventTestResult, map[string]any{
			"status": "skipped", "exit_code": 0, "output_tail": "", "duration_ms": 0,
		}); err != nil {
			return err
		}
		return w.move(domain.StateValidating, domain.StateReviewReady, "validation passed")
	}

	tctx, cancel := context.WithTimeout(ctx, w.timeout)
	defer cancel()
	run, err := runTestCmd(tctx, wt, w.project.TestCmd, w.env)
	if err != nil {
		return w.failTask(domain.StateValidating, ReasonWorkspaceError, err.Error())
	}
	status := "passed"
	if !run.Passed {
		status = "failed"
	}
	t, err = w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
		t.TestStatus = status
		t.TestOutput = run.Output
		return nil
	})
	if err != nil {
		return err
	}
	w.task = t
	if err := w.emit(domain.EventTestResult, map[string]any{
		"status":      status,
		"exit_code":   run.ExitCode,
		"output_tail": run.Output,
		"duration_ms": run.Duration.Milliseconds(),
	}); err != nil {
		return err
	}
	if !run.Passed {
		return w.failTask(domain.StateValidating, ReasonTestsFailed,
			fmt.Sprintf("tests failed with exit code %d", run.ExitCode))
	}
	return w.move(domain.StateValidating, domain.StateReviewReady, "validation passed")
}

// move performs a CAS transition from one state. If the task was cancelled concurrently it returns errCancelled.
func (w *runner) move(from, to domain.TaskState, reason string) error {
	t, err := w.st.TransitionTask(w.bg, w.task.ID, []domain.TaskState{from}, to, &store.EventInput{Reason: reason})
	if err != nil {
		if errors.Is(err, domain.ErrStateConflict) {
			if cur, gerr := w.st.GetTask(w.bg, w.task.ID); gerr == nil && cur.State == domain.StateCancelled {
				return errCancelled
			}
		}
		return err
	}
	w.task = t
	return nil
}

// failTask records the failure reason and moves the task to FAILED. The worktree is left in place.
func (w *runner) failTask(from domain.TaskState, reason, detail string) error {
	t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
		t.FailureReason = reason
		t.Error = redactText(detail)
		return nil
	})
	if err != nil {
		return err
	}
	w.task = t
	return w.move(from, domain.StateFailed, reason)
}

// retryOrFail counts an attempt. It moves to RETRYING while attempts remain and to FAILED otherwise.
func (w *runner) retryOrFail(from domain.TaskState, reason, detail string) error {
	t, err := w.st.MutateTask(w.bg, w.task.ID, func(t *domain.Task) error {
		t.Attempt++
		t.FailureReason = reason
		t.Error = redactText(detail)
		return nil
	})
	if err != nil {
		return err
	}
	w.task = t
	to := domain.StateRetrying
	if t.Attempt >= t.MaxAttempts {
		to = domain.StateFailed
	}
	return w.move(from, to, reason)
}

// emit appends one task event with a JSON object payload.
func (w *runner) emit(typ domain.EventType, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("worker: encode %s event: %w", typ, err)
	}
	_, err = w.st.AppendTaskEvent(w.bg, w.task.ID, store.NewEvent{Type: typ, Payload: raw})
	return err
}

// denialList builds the {tool, input} list used by permission_request events and pending_approval.
func denialList(ds []adapters.Denial) []map[string]any {
	out := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		out = append(out, map[string]any{"tool": d.Tool, "input": inputPayload(d.Input)})
	}
	return out
}

// inputPayload returns tool input as redacted JSON. Input that is not valid JSON after redaction is stored as a string.
func inputPayload(in json.RawMessage) any {
	if len(in) == 0 {
		return nil
	}
	red := redactText(string(in))
	if json.Valid([]byte(red)) {
		return json.RawMessage(red)
	}
	b, _ := json.Marshal(red)
	return json.RawMessage(b)
}

// mergeTools returns the union of the project and task tool grants, keeping order and dropping duplicates.
func mergeTools(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, tool := range list {
			if tool != "" && !seen[tool] {
				seen[tool] = true
				out = append(out, tool)
			}
		}
	}
	return out
}

func commitMessage(t domain.Task) string {
	title := oneLine(t.Title)
	if title == "" {
		title = "agent task"
	}
	return fmt.Sprintf("rhymehub: %s\n\nTask %s", title, t.ID)
}
