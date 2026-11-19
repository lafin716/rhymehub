package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
)

// queryer is satisfied by *sql.DB and *sql.Tx so helpers can run inside or outside a transaction.
type queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// taskSelect is the read column list (nullable text columns coalesced, timestamps scanned as NullString).
const taskSelect = `id, project_id, COALESCE(request_id, ''), title, prompt, agent, model, state, priority,
	branch, worktree_path, base_commit, head_commit, session_id, next_input, allowed_tools,
	pending_question, pending_approval, worker_pid, heartbeat_at, attempt, max_attempts, turns,
	test_status, test_output, diff_stat, result_summary, error, failure_reason, cost_usd,
	COALESCE(release_id, ''), created_by, COALESCE(approved_by, ''), version, created_at, updated_at,
	started_at, finished_at`

// taskInsertCols must list the same columns, in the same order, as the placeholders in CreateTask.
const taskInsertCols = `id, project_id, request_id, title, prompt, agent, model, state, priority,
	branch, worktree_path, base_commit, head_commit, session_id, next_input, allowed_tools,
	pending_question, pending_approval, worker_pid, heartbeat_at, attempt, max_attempts, turns,
	test_status, test_output, diff_stat, result_summary, error, failure_reason, cost_usd,
	release_id, created_by, approved_by, version, created_at, updated_at, started_at, finished_at`

// runningStates occupy scheduler execution slots.
var runningStates = []domain.TaskState{domain.StatePreparing, domain.StateRunning, domain.StateValidating}

// finishedStates get finished_at stamped when entered.
var finishedStates = map[domain.TaskState]bool{
	domain.StateCompleted:  true,
	domain.StateFailed:     true,
	domain.StateCancelled:  true,
	domain.StateRolledBack: true,
}

// placeholders returns "?,?,?" with n markers.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// stateArgs builds an IN (...) clause body and its arguments.
func stateArgs(states []domain.TaskState) (string, []any) {
	args := make([]any, len(states))
	for i, s := range states {
		args[i] = string(s)
	}
	return placeholders(len(states)), args
}

func scanTask(r rowScanner) (domain.Task, error) {
	var t domain.Task
	var tools, pendingApproval, state, created, updated string
	var heartbeat, started, finished sql.NullString
	err := r.Scan(&t.ID, &t.ProjectID, &t.RequestID, &t.Title, &t.Prompt, &t.Agent, &t.Model, &state,
		&t.Priority, &t.Branch, &t.WorktreePath, &t.BaseCommit, &t.HeadCommit, &t.SessionID, &t.NextInput,
		&tools, &t.PendingQuestion, &pendingApproval, &t.WorkerPID, &heartbeat, &t.Attempt, &t.MaxAttempts,
		&t.Turns, &t.TestStatus, &t.TestOutput, &t.DiffStat, &t.ResultSummary, &t.Error, &t.FailureReason,
		&t.CostUSD, &t.ReleaseID, &t.CreatedBy, &t.ApprovedBy, &t.Version, &created, &updated, &started, &finished)
	if err != nil {
		return domain.Task{}, err
	}
	t.State = domain.TaskState(state)
	if t.AllowedTools, err = decodeList(tools); err != nil {
		return domain.Task{}, err
	}
	if pendingApproval != "" {
		t.PendingApproval = json.RawMessage(pendingApproval)
	}
	if t.HeartbeatAt, err = scanNullTime(heartbeat); err != nil {
		return domain.Task{}, err
	}
	if t.StartedAt, err = scanNullTime(started); err != nil {
		return domain.Task{}, err
	}
	if t.FinishedAt, err = scanNullTime(finished); err != nil {
		return domain.Task{}, err
	}
	if t.CreatedAt, err = parseTime(created); err != nil {
		return domain.Task{}, err
	}
	if t.UpdatedAt, err = parseTime(updated); err != nil {
		return domain.Task{}, err
	}
	return t, nil
}

// loadTask reads one task by id using q (a *sql.DB or *sql.Tx).
func loadTask(ctx context.Context, q queryer, id string) (domain.Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, `SELECT `+taskSelect+` FROM tasks WHERE id = ?`, id))
	return t, wrapNotFound(err, "task "+id)
}

// GetTask loads a task by id.
func (s *Store) GetTask(ctx context.Context, id string) (domain.Task, error) {
	return loadTask(ctx, s.db, id)
}

// CreateTask inserts a new task. Tasks always start in CREATED; move them on with TransitionTask.
func (s *Store) CreateTask(ctx context.Context, t domain.Task) (domain.Task, error) {
	if t.State == "" {
		t.State = domain.StateCreated
	}
	if t.State != domain.StateCreated {
		return domain.Task{}, fmt.Errorf("%w: new task must start in CREATED, got %s", domain.ErrInvalid, t.State)
	}
	if t.ID == "" {
		t.ID = domain.NewID("tsk")
	}
	if t.MaxAttempts < 1 {
		t.MaxAttempts = 2
	}
	now := time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	t.UpdatedAt = now
	t.Version = 0
	tools, err := encodeList(t.AllowedTools)
	if err != nil {
		return domain.Task{}, err
	}
	pending := ""
	if len(t.PendingApproval) > 0 {
		pending = string(t.PendingApproval)
	}
	err = s.writeTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tasks(`+taskInsertCols+`) VALUES(`+placeholders(38)+`)`,
			t.ID, t.ProjectID, nullStr(t.RequestID), t.Title, t.Prompt, t.Agent, t.Model, string(t.State),
			t.Priority, t.Branch, t.WorktreePath, t.BaseCommit, t.HeadCommit, t.SessionID, t.NextInput, tools,
			t.PendingQuestion, pending, t.WorkerPID, nullTime(t.HeartbeatAt), t.Attempt, t.MaxAttempts, t.Turns,
			t.TestStatus, t.TestOutput, t.DiffStat, t.ResultSummary, t.Error, t.FailureReason, t.CostUSD,
			nullStr(t.ReleaseID), t.CreatedBy, nullStr(t.ApprovedBy), t.Version, fmtTime(t.CreatedAt),
			fmtTime(t.UpdatedAt), nullTime(t.StartedAt), nullTime(t.FinishedAt))
		return duplicateErr(err, "task "+t.ID)
	})
	return t, err
}

// ListTasks returns tasks of a project (newest first). Empty projectID lists all projects.
func (s *Store) ListTasks(ctx context.Context, projectID string, limit int) ([]domain.Task, error) {
	q := `SELECT ` + taskSelect + ` FROM tasks`
	var args []any
	if projectID != "" {
		q += ` WHERE project_id = ?`
		args = append(args, projectID)
	}
	q += ` ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EventInput is optional metadata for a state transition. It is stored in the state_changed event.
type EventInput struct {
	// Reason is a short human-readable explanation of the transition.
	Reason string
	// IdemKey makes the transition idempotent: if an event with this key already exists for the task,
	// TransitionTask returns the current task with a nil error and changes nothing.
	IdemKey string
	// Detail is extra JSON stored under "detail" in the event payload.
	Detail json.RawMessage
}

// TransitionTask atomically moves a task from one of expectFrom (any state when empty) to `to`.
// In one write transaction it checks the idempotency key, checks the current state against expectFrom,
// validates the edge with domain.CanTransition, performs a version-checked compare-and-set on tasks, and
// appends a state_changed event with the next per-task seq.
//
// Errors: domain.ErrNotFound (no task), domain.ErrStateConflict (current state not in expectFrom or lost
// a race), domain.ErrIllegalTransition (edge not in the state machine).
func (s *Store) TransitionTask(ctx context.Context, id string, expectFrom []domain.TaskState, to domain.TaskState, ev *EventInput) (domain.Task, error) {
	var out domain.Task
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		t, err := transitionTx(ctx, tx, id, expectFrom, to, ev, time.Now())
		out = t
		return err
	})
	return out, err
}

func transitionTx(ctx context.Context, tx *sql.Tx, id string, expectFrom []domain.TaskState, to domain.TaskState, ev *EventInput, now time.Time) (domain.Task, error) {
	if !to.Valid() {
		return domain.Task{}, fmt.Errorf("%w: unknown target state %q", domain.ErrInvalid, to)
	}
	t, err := loadTask(ctx, tx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if ev != nil && ev.IdemKey != "" {
		seen, err := idemSeen(ctx, tx, id, ev.IdemKey)
		if err != nil {
			return domain.Task{}, err
		}
		if seen {
			return t, nil // duplicate request: no-op
		}
	}
	from := t.State
	if len(expectFrom) > 0 && !containsState(expectFrom, from) {
		return domain.Task{}, fmt.Errorf("%w: task %s is %s, expected one of %v", domain.ErrStateConflict, id, from, expectFrom)
	}
	if err := domain.CheckTransition(from, to); err != nil {
		return domain.Task{}, err
	}
	started := t.StartedAt
	if to == domain.StatePreparing && started == nil {
		started = &now
	}
	finished := t.FinishedAt
	if finishedStates[to] {
		finished = &now
	}
	res, err := tx.ExecContext(ctx, `UPDATE tasks SET state = ?, version = version + 1, updated_at = ?,
		started_at = ?, finished_at = ? WHERE id = ? AND state = ? AND version = ?`,
		string(to), fmtTime(now), nullTime(started), nullTime(finished), id, string(from), t.Version)
	if err != nil {
		return domain.Task{}, fmt.Errorf("store: transition %s: %w", id, err)
	}
	if err := expectOne(res, "task "+id+" lost CAS race"); err != nil {
		return domain.Task{}, fmt.Errorf("%w: task %s changed concurrently", domain.ErrStateConflict, id)
	}
	payload := map[string]any{"from": from, "to": to}
	var idem string
	if ev != nil {
		if ev.Reason != "" {
			payload["reason"] = ev.Reason
		}
		if len(ev.Detail) > 0 {
			payload["detail"] = ev.Detail
		}
		idem = ev.IdemKey
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return domain.Task{}, fmt.Errorf("store: encode event: %w", err)
	}
	if _, err := appendEventTx(ctx, tx, id, domain.EventStateChanged, raw, idem, now); err != nil {
		return domain.Task{}, err
	}
	return loadTask(ctx, tx, id)
}

func containsState(list []domain.TaskState, s domain.TaskState) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// MutateTask applies fn to a copy of the task inside a write transaction and stores the non-state fields
// (version is bumped). Changing State inside fn is rejected; use TransitionTask for state changes.
func (s *Store) MutateTask(ctx context.Context, id string, fn func(t *domain.Task) error) (domain.Task, error) {
	var out domain.Task
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		t, err := loadTask(ctx, tx, id)
		if err != nil {
			return err
		}
		orig := t.State
		if err := fn(&t); err != nil {
			return err
		}
		if t.State != orig {
			return fmt.Errorf("%w: MutateTask cannot change state; use TransitionTask", domain.ErrInvalid)
		}
		tools, err := encodeList(t.AllowedTools)
		if err != nil {
			return err
		}
		pending := ""
		if len(t.PendingApproval) > 0 {
			pending = string(t.PendingApproval)
		}
		now := time.Now()
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET title=?, prompt=?, agent=?, model=?, priority=?,
			branch=?, worktree_path=?, base_commit=?, head_commit=?, session_id=?, next_input=?, allowed_tools=?,
			pending_question=?, pending_approval=?, worker_pid=?, heartbeat_at=?, attempt=?, max_attempts=?,
			turns=?, test_status=?, test_output=?, diff_stat=?, result_summary=?, error=?, failure_reason=?,
			cost_usd=?, release_id=?, approved_by=?, started_at=?, finished_at=?, version = version + 1, updated_at=?
			WHERE id=? AND version=?`,
			t.Title, t.Prompt, t.Agent, t.Model, t.Priority, t.Branch, t.WorktreePath, t.BaseCommit, t.HeadCommit,
			t.SessionID, t.NextInput, tools, t.PendingQuestion, pending, t.WorkerPID, nullTime(t.HeartbeatAt),
			t.Attempt, t.MaxAttempts, t.Turns, t.TestStatus, t.TestOutput, t.DiffStat, t.ResultSummary, t.Error,
			t.FailureReason, t.CostUSD, nullStr(t.ReleaseID), nullStr(t.ApprovedBy), nullTime(t.StartedAt),
			nullTime(t.FinishedAt), fmtTime(now), id, t.Version)
		if err != nil {
			return err
		}
		if err := expectOne(res, "task "+id+" lost version race"); err != nil {
			return fmt.Errorf("%w: task %s changed concurrently", domain.ErrStateConflict, id)
		}
		out, err = loadTask(ctx, tx, id)
		return err
	})
	return out, err
}

// --- events -----------------------------------------------------------------------------------

// NewEvent describes an event to append with AppendTaskEvent. state_changed events are produced only by TransitionTask.
type NewEvent struct {
	Type    domain.EventType
	Payload json.RawMessage
	// IdemKey optionally dedupes the event per task. A repeated key returns the existing event.
	IdemKey string
}

// AppendTaskEvent appends a non-state event to a task's log with the next per-task seq.
func (s *Store) AppendTaskEvent(ctx context.Context, taskID string, ev NewEvent) (domain.TaskEvent, error) {
	if !ev.Type.Valid() {
		return domain.TaskEvent{}, fmt.Errorf("%w: event type %q", domain.ErrInvalid, ev.Type)
	}
	if ev.Type == domain.EventStateChanged {
		return domain.TaskEvent{}, fmt.Errorf("%w: state_changed is written by TransitionTask", domain.ErrInvalid)
	}
	var out domain.TaskEvent
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		if _, err := loadTask(ctx, tx, taskID); err != nil {
			return err
		}
		if ev.IdemKey != "" {
			existing, found, err := findIdemEvent(ctx, tx, taskID, ev.IdemKey)
			if err != nil {
				return err
			}
			if found {
				out = existing
				return nil
			}
		}
		e, err := appendEventTx(ctx, tx, taskID, ev.Type, ev.Payload, ev.IdemKey, time.Now())
		out = e
		return err
	})
	return out, err
}

func idemSeen(ctx context.Context, tx *sql.Tx, taskID, key string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_events WHERE task_id = ? AND idem_key = ?`, taskID, key).Scan(&n)
	return n > 0, err
}

func findIdemEvent(ctx context.Context, tx *sql.Tx, taskID, key string) (domain.TaskEvent, bool, error) {
	var e domain.TaskEvent
	var typ, payload, created string
	err := tx.QueryRowContext(ctx, `SELECT id, task_id, seq, type, payload, created_at FROM task_events
		WHERE task_id = ? AND idem_key = ?`, taskID, key).
		Scan(&e.ID, &e.TaskID, &e.Seq, &typ, &payload, &created)
	if err == sql.ErrNoRows {
		return domain.TaskEvent{}, false, nil
	}
	if err != nil {
		return domain.TaskEvent{}, false, err
	}
	e.Type = domain.EventType(typ)
	e.Payload = json.RawMessage(payload)
	if e.CreatedAt, err = parseTime(created); err != nil {
		return domain.TaskEvent{}, false, err
	}
	return e, true, nil
}

// appendEventTx inserts an event with seq = max(seq)+1 for the task. Must run inside a write transaction.
func appendEventTx(ctx context.Context, tx *sql.Tx, taskID string, typ domain.EventType, payload json.RawMessage, idem string, at time.Time) (domain.TaskEvent, error) {
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM task_events WHERE task_id = ?`, taskID).Scan(&seq); err != nil {
		return domain.TaskEvent{}, fmt.Errorf("store: next seq: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO task_events(task_id, seq, type, payload, idem_key, created_at)
		VALUES(?,?,?,?,?,?)`, taskID, seq, string(typ), string(payload), nullStr(idem), fmtTime(at))
	if err != nil {
		return domain.TaskEvent{}, fmt.Errorf("store: append event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.TaskEvent{}, err
	}
	return domain.TaskEvent{ID: id, TaskID: taskID, Seq: seq, Type: typ, Payload: payload, IdemKey: idem, CreatedAt: at}, nil
}

// ListEventsSince returns events with global id greater than afterID, oldest first, at most limit (0 = 500).
// taskID empty means all tasks. Use the returned last ID as the SSE Last-Event-ID cursor.
func (s *Store) ListEventsSince(ctx context.Context, taskID string, afterID int64, limit int) ([]domain.TaskEvent, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT id, task_id, seq, type, payload, created_at FROM task_events WHERE id > ?`
	args := []any{afterID}
	if taskID != "" {
		q += ` AND task_id = ?`
		args = append(args, taskID)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskEvent
	for rows.Next() {
		var e domain.TaskEvent
		var typ, payload, created string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Seq, &typ, &payload, &created); err != nil {
			return nil, err
		}
		e.Type = domain.EventType(typ)
		e.Payload = json.RawMessage(payload)
		if e.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- scheduler helpers ------------------------------------------------------------------------

// ClaimLimits are the scheduler's concurrency limits. A value <= 0 for Global or DefaultPerAgent means unlimited.
// PerAgent overrides DefaultPerAgent for specific agent names. Per-project limits come from projects.max_concurrent
// (values below 1 are treated as 1).
type ClaimLimits struct {
	Global          int
	DefaultPerAgent int
	PerAgent        map[string]int
}

// ClaimNextQueued picks the oldest highest-priority QUEUED task whose project, agent and global slots are
// free, and moves it QUEUED -> PREPARING in the same write transaction. It returns ok=false when nothing can
// be claimed. Running slots are PREPARING, RUNNING and VALIDATING tasks.
func (s *Store) ClaimNextQueued(ctx context.Context, lim ClaimLimits) (domain.Task, bool, error) {
	var out domain.Task
	var ok bool
	err := s.writeTx(ctx, func(tx *sql.Tx) error {
		now := time.Now()
		args, runArgs := stateArgs(runningStates)
		rows, err := tx.QueryContext(ctx, `SELECT project_id, agent, COUNT(*) FROM tasks
			WHERE state IN (`+args+`) GROUP BY project_id, agent`, runArgs...)
		if err != nil {
			return err
		}
		global := 0
		perProject := map[string]int{}
		perAgent := map[string]int{}
		for rows.Next() {
			var pid, agent string
			var n int
			if err := rows.Scan(&pid, &agent, &n); err != nil {
				rows.Close()
				return err
			}
			global += n
			perProject[pid] += n
			perAgent[agent] += n
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if lim.Global > 0 && global >= lim.Global {
			return nil
		}
		queued, err := tx.QueryContext(ctx, `SELECT t.id, t.project_id, t.agent, p.max_concurrent
			FROM tasks t JOIN projects p ON p.id = t.project_id
			WHERE t.state = ? ORDER BY t.priority, t.created_at, t.id`, string(domain.StateQueued))
		if err != nil {
			return err
		}
		type cand struct {
			id, pid, agent string
			maxConc        int
		}
		var cands []cand
		for queued.Next() {
			var c cand
			if err := queued.Scan(&c.id, &c.pid, &c.agent, &c.maxConc); err != nil {
				queued.Close()
				return err
			}
			cands = append(cands, c)
		}
		if err := queued.Close(); err != nil {
			return err
		}
		for _, c := range cands {
			maxP := c.maxConc
			if maxP < 1 {
				maxP = 1
			}
			if perProject[c.pid] >= maxP {
				continue
			}
			agentLimit := lim.DefaultPerAgent
			if v, found := lim.PerAgent[c.agent]; found {
				agentLimit = v
			}
			if agentLimit > 0 && perAgent[c.agent] >= agentLimit {
				continue
			}
			t, err := transitionTx(ctx, tx, c.id, []domain.TaskState{domain.StateQueued}, domain.StatePreparing,
				&EventInput{Reason: "claimed by scheduler"}, now)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET heartbeat_at = ? WHERE id = ?`, fmtTime(now), c.id); err != nil {
				return err
			}
			out, err = loadTask(ctx, tx, t.ID)
			if err != nil {
				return err
			}
			ok = true
			return nil
		}
		return nil
	})
	return out, ok, err
}

// UpdateHeartbeat records a worker heartbeat and its pid for a task that is in a running state.
func (s *Store) UpdateHeartbeat(ctx context.Context, id string, pid int, at time.Time) error {
	args, runArgs := stateArgs(runningStates)
	params := append([]any{fmtTime(at), pid, id}, runArgs...)
	return s.writeTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET heartbeat_at = ?, worker_pid = ?
			WHERE id = ? AND state IN (`+args+`)`, params...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: task %s is not running", domain.ErrStateConflict, id)
		}
		return nil
	})
}

// ListOrphans returns running-state tasks whose heartbeat is missing or older than staleBefore.
func (s *Store) ListOrphans(ctx context.Context, staleBefore time.Time) ([]domain.Task, error) {
	args, runArgs := stateArgs(runningStates)
	params := append(runArgs, fmtTime(staleBefore))
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskSelect+` FROM tasks
		WHERE state IN (`+args+`) AND (heartbeat_at IS NULL OR heartbeat_at < ?) ORDER BY created_at, id`, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountByState returns the number of tasks per state.
func (s *Store) CountByState(ctx context.Context) (map[domain.TaskState]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM tasks GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[domain.TaskState]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[domain.TaskState(st)] = n
	}
	return out, rows.Err()
}

// CountRunning returns how many tasks occupy an execution slot (PREPARING, RUNNING, VALIDATING).
func (s *Store) CountRunning(ctx context.Context) (int, error) {
	args, runArgs := stateArgs(runningStates)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE state IN (`+args+`)`, runArgs...).Scan(&n)
	return n, err
}
