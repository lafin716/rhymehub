package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lafin716/rhymehub/internal/domain"
)

// newTestStore opens a fresh database in a temp dir and applies all migrations.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func newProject(t *testing.T, s *Store, maxConcurrent int) domain.Project {
	t.Helper()
	p, err := s.CreateProject(context.Background(), domain.Project{
		Name: "proj", RepoPath: t.TempDir(), MaxConcurrent: maxConcurrent,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

func newTask(t *testing.T, s *Store, projectID, agent string, priority int) domain.Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), domain.Task{
		ProjectID: projectID, Title: "t", Prompt: "p", Agent: agent, Priority: priority, CreatedBy: "test",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

// queue moves a CREATED task to QUEUED.
func queue(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.TransitionTask(context.Background(), id, nil, domain.StateQueued, nil); err != nil {
		t.Fatalf("queue %s: %v", id, err)
	}
}

// --- migrations ------------------------------------------------------------------------------

func TestMigrateFreshAndRerun(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if v, err := s.SchemaVersion(ctx); err != nil || v != 0 {
		t.Fatalf("fresh SchemaVersion = %d, %v; want 0, nil", v, err)
	}
	migs, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) == 0 {
		t.Fatal("no embedded migrations")
	}
	want := migs[len(migs)-1].Version

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if v, _ := s.SchemaVersion(ctx); v != want {
		t.Fatalf("SchemaVersion after migrate = %d, want %d", v, want)
	}
	// Re-running must be a no-op.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("re-run migrate: %v", err)
	}
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(migs) {
		t.Fatalf("schema_migrations rows = %d, want %d", n, len(migs))
	}
}

func TestMigrateConcurrentCallers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "race.db")
	var stores []*Store
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores = append(stores, s)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(stores))
	for i, s := range stores {
		wg.Add(1)
		go func(i int, s *Store) {
			defer wg.Done()
			errs[i] = s.Migrate(ctx)
		}(i, s)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
}

// --- task creation and CAS -------------------------------------------------------------------

func TestCreateTaskStartsCreatedAndRejectsOtherStates(t *testing.T) {
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)
	if task.State != domain.StateCreated || task.MaxAttempts != 2 {
		t.Fatalf("new task = state %s max %d", task.State, task.MaxAttempts)
	}
	_, err := s.CreateTask(context.Background(), domain.Task{
		ProjectID: p.ID, Agent: "claude", State: domain.StateQueued, CreatedBy: "test",
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("non-CREATED insert: err = %v, want ErrInvalid", err)
	}
}

func TestTransitionTaskHappyPathAndEvents(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)

	got, err := s.TransitionTask(ctx, task.ID, []domain.TaskState{domain.StateCreated}, domain.StateQueued,
		&EventInput{Reason: "submitted"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != domain.StateQueued || got.Version != task.Version+1 {
		t.Fatalf("after transition state=%s version=%d", got.State, got.Version)
	}
	events, err := s.ListEventsSince(ctx, task.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != domain.EventStateChanged || events[0].Seq != 1 {
		t.Fatalf("events = %+v", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["from"] != "CREATED" || payload["to"] != "QUEUED" || payload["reason"] != "submitted" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestTransitionTaskErrors(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)

	if _, err := s.TransitionTask(ctx, "tsk_missing", nil, domain.StateQueued, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing task: err = %v, want ErrNotFound", err)
	}
	if _, err := s.TransitionTask(ctx, task.ID, nil, domain.StateRunning, nil); !errors.Is(err, domain.ErrIllegalTransition) {
		t.Errorf("CREATED->RUNNING: err = %v, want ErrIllegalTransition", err)
	}
	if _, err := s.TransitionTask(ctx, task.ID, []domain.TaskState{domain.StateQueued}, domain.StateCancelled, nil); !errors.Is(err, domain.ErrStateConflict) {
		t.Errorf("expectFrom mismatch: err = %v, want ErrStateConflict", err)
	}
	if _, err := s.TransitionTask(ctx, task.ID, nil, domain.TaskState("NOPE"), nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown target: err = %v, want ErrInvalid", err)
	}
	// Failed attempts must not change the task.
	after, _ := s.GetTask(ctx, task.ID)
	if after.State != domain.StateCreated || after.Version != task.Version {
		t.Fatalf("failed transitions mutated task: state=%s version=%d", after.State, after.Version)
	}
}

func TestTransitionCASRaceOnlyOneWins(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)
	queue(t, s, task.ID)

	const workers = 2
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = s.TransitionTask(ctx, task.ID, []domain.TaskState{domain.StateQueued},
				domain.StatePreparing, &EventInput{IdemKey: "claim-" + string(rune('a'+i))})
		}(i)
	}
	close(start)
	wg.Wait()

	wins, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, domain.ErrStateConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d, want 1 and 1", wins, conflicts)
	}
	final, _ := s.GetTask(ctx, task.ID)
	if final.State != domain.StatePreparing || final.Version != 2 {
		t.Fatalf("final state=%s version=%d", final.State, final.Version)
	}
	events, _ := s.ListEventsSince(ctx, task.ID, 0, 0)
	stateEvents := 0
	for _, e := range events {
		if e.Type == domain.EventStateChanged {
			stateEvents++
		}
	}
	// queue (1) + single winning claim (1).
	if stateEvents != 2 {
		t.Fatalf("state_changed events = %d, want 2", stateEvents)
	}
}

func TestTransitionIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)

	first, err := s.TransitionTask(ctx, task.ID, nil, domain.StateQueued, &EventInput{IdemKey: "submit-1"})
	if err != nil {
		t.Fatal(err)
	}
	// Retry with the same key: no-op, nil error, returns the current task.
	again, err := s.TransitionTask(ctx, task.ID, nil, domain.StateCancelled, &EventInput{IdemKey: "submit-1"})
	if err != nil {
		t.Fatalf("duplicate key must be a no-op, got %v", err)
	}
	if again.State != domain.StateQueued || again.Version != first.Version {
		t.Fatalf("duplicate changed task: state=%s version=%d", again.State, again.Version)
	}
	events, _ := s.ListEventsSince(ctx, task.ID, 0, 0)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func TestMutateTaskRejectsStateChange(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)

	_, err := s.MutateTask(ctx, task.ID, func(t *domain.Task) error {
		t.State = domain.StateQueued
		return nil
	})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("state change in MutateTask: err = %v, want ErrInvalid", err)
	}
	out, err := s.MutateTask(ctx, task.ID, func(t *domain.Task) error {
		t.Title = "renamed"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "renamed" || out.Version != task.Version+1 {
		t.Fatalf("mutate result title=%q version=%d", out.Title, out.Version)
	}
}

// --- scheduler claims ------------------------------------------------------------------------

func TestClaimNextQueuedGlobalLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 10)
	for i := 0; i < 3; i++ {
		queue(t, s, newTask(t, s, p.ID, "claude", 2).ID)
	}
	lim := ClaimLimits{Global: 2, DefaultPerAgent: 0}
	for i := 0; i < 2; i++ {
		_, ok, err := s.ClaimNextQueued(ctx, lim)
		if err != nil || !ok {
			t.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
		}
	}
	if _, ok, err := s.ClaimNextQueued(ctx, lim); err != nil || ok {
		t.Fatalf("third claim must be refused by global limit: ok=%v err=%v", ok, err)
	}
	if n, _ := s.CountRunning(ctx); n != 2 {
		t.Fatalf("CountRunning = %d, want 2", n)
	}
}

func TestClaimNextQueuedPerProjectLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	queue(t, s, newTask(t, s, p.ID, "claude", 2).ID)
	queue(t, s, newTask(t, s, p.ID, "codex", 2).ID)

	// max_concurrent = 1 on the project; no agent limits.
	if _, ok, err := s.ClaimNextQueued(ctx, ClaimLimits{}); err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.ClaimNextQueued(ctx, ClaimLimits{}); err != nil || ok {
		t.Fatalf("second claim must respect project max_concurrent: ok=%v err=%v", ok, err)
	}
}

func TestClaimNextQueuedPerAgentLimit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 10)
	queue(t, s, newTask(t, s, p.ID, "claude", 2).ID)
	queue(t, s, newTask(t, s, p.ID, "claude", 2).ID)
	queue(t, s, newTask(t, s, p.ID, "codex", 2).ID)

	lim := ClaimLimits{DefaultPerAgent: 1}
	first, ok, err := s.ClaimNextQueued(ctx, lim)
	if err != nil || !ok || first.Agent != "claude" {
		t.Fatalf("first claim: agent=%q ok=%v err=%v", first.Agent, ok, err)
	}
	// Second claude task is blocked; the codex task is still claimable.
	second, ok, err := s.ClaimNextQueued(ctx, lim)
	if err != nil || !ok || second.Agent != "codex" {
		t.Fatalf("second claim should skip blocked claude: agent=%q ok=%v err=%v", second.Agent, ok, err)
	}
	if _, ok, _ := s.ClaimNextQueued(ctx, lim); ok {
		t.Fatal("no further claims allowed under per-agent limit")
	}
}

func TestClaimNextQueuedPerAgentOverride(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 10)
	queue(t, s, newTask(t, s, p.ID, "claude", 2).ID)
	queue(t, s, newTask(t, s, p.ID, "claude", 2).ID)

	lim := ClaimLimits{DefaultPerAgent: 1, PerAgent: map[string]int{"claude": 2}}
	for i := 0; i < 2; i++ {
		if _, ok, err := s.ClaimNextQueued(ctx, lim); err != nil || !ok {
			t.Fatalf("claim %d under override: ok=%v err=%v", i, ok, err)
		}
	}
}

func TestClaimNextQueuedPriorityOrder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 10)
	low := newTask(t, s, p.ID, "claude", 3)
	high := newTask(t, s, p.ID, "claude", 1)
	queue(t, s, low.ID)
	queue(t, s, high.ID)

	got, ok, err := s.ClaimNextQueued(ctx, ClaimLimits{})
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if got.ID != high.ID || got.State != domain.StatePreparing || got.HeartbeatAt == nil {
		t.Fatalf("claimed %s (state %s), want high-priority %s in PREPARING with heartbeat", got.ID, got.State, high.ID)
	}
}

func TestClaimNextQueuedNothingQueued(t *testing.T) {
	s := newTestStore(t)
	if _, ok, err := s.ClaimNextQueued(context.Background(), ClaimLimits{}); err != nil || ok {
		t.Fatalf("empty queue: ok=%v err=%v", ok, err)
	}
}

// --- event log -------------------------------------------------------------------------------

func TestAppendTaskEventSeqAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)

	e1, err := s.AppendTaskEvent(ctx, task.ID, NewEvent{Type: domain.EventAgentText, Payload: json.RawMessage(`{"text":"a"}`)})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := s.AppendTaskEvent(ctx, task.ID, NewEvent{Type: domain.EventLog, IdemKey: "log-1"})
	if err != nil {
		t.Fatal(err)
	}
	if e1.Seq != 1 || e2.Seq != 2 || e2.ID <= e1.ID {
		t.Fatalf("seq/id not monotonic: e1=%+v e2=%+v", e1, e2)
	}
	dup, err := s.AppendTaskEvent(ctx, task.ID, NewEvent{Type: domain.EventLog, IdemKey: "log-1"})
	if err != nil {
		t.Fatal(err)
	}
	if dup.ID != e2.ID || dup.Seq != e2.Seq {
		t.Fatalf("duplicate idem key must return existing event: got %+v want %+v", dup, e2)
	}
	events, _ := s.ListEventsSince(ctx, task.ID, 0, 0)
	if len(events) != 2 {
		t.Fatalf("events after dedupe = %d, want 2", len(events))
	}
	if string(events[0].Payload) != `{"text":"a"}` {
		t.Fatalf("payload not preserved: %s", events[0].Payload)
	}
}

func TestAppendTaskEventRejects(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	task := newTask(t, s, p.ID, "claude", 2)

	if _, err := s.AppendTaskEvent(ctx, task.ID, NewEvent{Type: domain.EventStateChanged}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("state_changed via append: err = %v, want ErrInvalid", err)
	}
	if _, err := s.AppendTaskEvent(ctx, task.ID, NewEvent{Type: domain.EventType("bogus")}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("unknown type: err = %v, want ErrInvalid", err)
	}
	if _, err := s.AppendTaskEvent(ctx, "tsk_missing", NewEvent{Type: domain.EventLog}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing task: err = %v, want ErrNotFound", err)
	}
}

func TestListEventsSinceCursor(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := newProject(t, s, 1)
	a := newTask(t, s, p.ID, "claude", 2)
	b := newTask(t, s, p.ID, "claude", 2)
	for i := 0; i < 3; i++ {
		if _, err := s.AppendTaskEvent(ctx, a.ID, NewEvent{Type: domain.EventLog}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendTaskEvent(ctx, b.ID, NewEvent{Type: domain.EventLog}); err != nil {
		t.Fatal(err)
	}

	page1, err := s.ListEventsSince(ctx, a.ID, 0, 2)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1 len=%d err=%v", len(page1), err)
	}
	page2, err := s.ListEventsSince(ctx, a.ID, page1[len(page1)-1].ID, 2)
	if err != nil || len(page2) != 1 {
		t.Fatalf("page2 len=%d err=%v", len(page2), err)
	}
	if page2[0].Seq != 3 {
		t.Fatalf("page2 seq = %d, want 3", page2[0].Seq)
	}
	if rest, _ := s.ListEventsSince(ctx, a.ID, page2[0].ID, 0); len(rest) != 0 {
		t.Fatalf("cursor at end must return nothing, got %d", len(rest))
	}
	// Empty task filter returns events from all tasks, in global id order.
	all, err := s.ListEventsSince(ctx, "", 0, 0)
	if err != nil || len(all) != 4 {
		t.Fatalf("all-task list len=%d err=%v", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatal("events not in ascending id order")
		}
	}
}

func TestGetTaskNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetTask(context.Background(), "tsk_nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
