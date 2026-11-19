package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
)

// fakeReleaser stands in for the release manager. On success it moves MERGING -> COMPLETED, as the
// Releaser contract allows when release is false; the error path lets the server mark the task FAILED.
type fakeReleaser struct {
	st    *store.Store
	err   error
	calls chan releaseCall
}

type releaseCall struct {
	id      string
	release bool
}

func newFakeReleaser() *fakeReleaser {
	return &fakeReleaser{calls: make(chan releaseCall, 10)}
}

func (f *fakeReleaser) ReleaseTask(ctx context.Context, id string, release bool) error {
	var err error
	if f.err != nil {
		err = f.err
	} else {
		_, err = f.st.TransitionTask(ctx, id, []domain.TaskState{domain.StateMerging}, domain.StateCompleted,
			&store.EventInput{Reason: "stub release"})
	}
	f.calls <- releaseCall{id: id, release: release}
	return err
}

type fakeCanceller struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeCanceller) CancelTask(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return nil
}

// newTaskEnv opens a fresh store in a temp dir, injects the fake collaborators, and creates the admin.
func newTaskEnv(t *testing.T, rel *fakeReleaser) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rhymehub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	srv, err := New(Options{
		Store: st, Config: config.Default(), Version: "test",
		Releaser: rel, Canceller: &fakeCanceller{},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	rel.st = st
	e := &testEnv{t: t, h: srv.Handler(), srv: srv}
	e.setupAdmin("root")
	return e
}

func (e *testEnv) session(username string) *http.Cookie {
	e.t.Helper()
	return sessionCookieFrom(e.t, e.login(username, testPassword))
}

func (e *testEnv) makeUser(admin *http.Cookie, username, role string) {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": username, "password": testPassword, "role": role,
	}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create %s: %d %s", role, rec.Code, rec.Body.String())
	}
}

func (e *testEnv) makeProject(id string) {
	e.t.Helper()
	_, err := e.srv.st.CreateProject(context.Background(), domain.Project{
		ID: id, Name: "Demo " + id, RepoPath: "/tmp/demo", DefaultBranch: "main",
		DefaultAgent: "claude", MaxConcurrent: 2, TimeoutMinutes: 30,
	})
	if err != nil {
		e.t.Fatalf("create project: %v", err)
	}
}

func (e *testEnv) createTask(cookie *http.Cookie, projectID, prompt string) string {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/tasks", map[string]string{"projectId": projectID, "prompt": prompt},
		withCookie(cookie), withCSRF())
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create task: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID == "" {
		e.t.Fatalf("decode task: %v %s", err, rec.Body.String())
	}
	if out.State != string(domain.StateQueued) {
		e.t.Fatalf("new task state = %s, want QUEUED", out.State)
	}
	return out.ID
}

// advanceTo drives a task through the worker-owned states. The worker is not in this test, so the
// transitions are applied directly through the same helper the handlers use.
func (e *testEnv) advanceTo(id string, path ...domain.TaskState) {
	e.t.Helper()
	ctx := context.Background()
	cur := domain.StateQueued
	for _, next := range path {
		if _, err := e.srv.transition(ctx, id, []domain.TaskState{cur}, next, &store.EventInput{Reason: "test"}); err != nil {
			e.t.Fatalf("advance %s -> %s: %v", cur, next, err)
		}
		cur = next
	}
}

func (e *testEnv) waitState(id string, want domain.TaskState) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		t, err := e.srv.st.GetTask(context.Background(), id)
		if err != nil {
			e.t.Fatalf("get task: %v", err)
		}
		if t.State == want {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("task %s state = %s, want %s", id, t.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTaskLifecycleAndFollowUp(t *testing.T) {
	rel := newFakeReleaser()
	e := newTaskEnv(t, rel)
	e.makeUser(e.session("root"), "dev1", "developer")
	dev := e.session("dev1")
	e.makeProject("demo")

	id := e.createTask(dev, "demo", "fix the login bug")

	// A follow-up is refused while the task is still queued.
	rec := e.do(http.MethodPost, "/api/v1/tasks/"+id+"/messages", map[string]string{"text": "more"},
		withCookie(dev), withCSRF())
	if rec.Code != http.StatusConflict {
		t.Fatalf("message from QUEUED: %d %s", rec.Code, rec.Body.String())
	}

	e.advanceTo(id, domain.StatePreparing, domain.StateRunning, domain.StateValidating, domain.StateReviewReady)

	rec = e.do(http.MethodPost, "/api/v1/tasks/"+id+"/messages", map[string]string{"text": "also cover edge cases"},
		withCookie(dev), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("message from REVIEW_READY: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"state":"QUEUED"`) {
		t.Fatalf("follow-up should requeue: %s", rec.Body.String())
	}

	rec = e.do(http.MethodGet, "/api/v1/tasks/"+id+"/events", nil, withCookie(dev))
	var evs struct {
		Events []struct {
			Seq  int64  `json:"seq"`
			Type string `json:"type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &evs); err != nil || len(evs.Events) < 5 {
		t.Fatalf("events: %d %s", rec.Code, rec.Body.String())
	}
	cursor := evs.Events[0].Seq
	rec = e.do(http.MethodGet, "/api/v1/tasks/"+id+"/events?after="+itoa(cursor), nil, withCookie(dev))
	if strings.Contains(rec.Body.String(), `"seq":`+itoa(cursor)+`,`) {
		t.Fatalf("after cursor must exclude the cursor event: %s", rec.Body.String())
	}

	rec = e.do(http.MethodGet, "/api/v1/tasks?projectId=demo&active=1", nil, withCookie(dev))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("active list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestApproveRequiresMaintainerAndReleases(t *testing.T) {
	rel := newFakeReleaser()
	e := newTaskEnv(t, rel)
	admin := e.session("root")
	e.makeUser(admin, "dev1", "developer")
	e.makeUser(admin, "maint1", "maintainer")
	dev := e.session("dev1")
	maint := e.session("maint1")
	e.makeProject("demo")

	id := e.createTask(dev, "demo", "add dark mode")
	e.advanceTo(id, domain.StatePreparing, domain.StateRunning, domain.StateValidating, domain.StateReviewReady)

	rec := e.do(http.MethodPost, "/api/v1/tasks/"+id+"/approve", map[string]bool{"release": true},
		withCookie(dev), withCSRF())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer approve: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodPost, "/api/v1/tasks/"+id+"/approve", map[string]bool{"release": true},
		withCookie(maint), withCSRF())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"MERGING"`) {
		t.Fatalf("maintainer approve: %d %s", rec.Code, rec.Body.String())
	}

	select {
	case call := <-rel.calls:
		if call.id != id || !call.release {
			t.Fatalf("releaser called with %+v", call)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("releaser was not called")
	}
	e.waitState(id, domain.StateCompleted)
}

func TestApproveRefusedWhenTestsFailed(t *testing.T) {
	rel := newFakeReleaser()
	e := newTaskEnv(t, rel)
	admin := e.session("root")
	e.makeUser(admin, "maint1", "maintainer")
	maint := e.session("maint1")
	e.makeProject("demo")

	id := e.createTask(admin, "demo", "break things")
	e.advanceTo(id, domain.StatePreparing, domain.StateRunning, domain.StateValidating, domain.StateReviewReady)
	if _, err := e.srv.st.MutateTask(context.Background(), id, func(t *domain.Task) error {
		t.TestStatus = "failed"
		return nil
	}); err != nil {
		t.Fatalf("mark tests failed: %v", err)
	}

	rec := e.do(http.MethodPost, "/api/v1/tasks/"+id+"/approve", map[string]bool{"release": false},
		withCookie(maint), withCSRF())
	if rec.Code != http.StatusConflict {
		t.Fatalf("approve with failed tests: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := e.srv.st.GetTask(context.Background(), id)
	if got.State != domain.StateReviewReady {
		t.Fatalf("state changed despite refusal: %s", got.State)
	}
}

func TestReleaseFailureMovesTaskToFailedWithoutLeakingError(t *testing.T) {
	rel := newFakeReleaser()
	rel.err = errors.New("secret-internal-detail")
	e := newTaskEnv(t, rel)
	admin := e.session("root")
	e.makeProject("demo")

	id := e.createTask(admin, "demo", "ship it")
	e.advanceTo(id, domain.StatePreparing, domain.StateRunning, domain.StateValidating, domain.StateReviewReady)

	rec := e.do(http.MethodPost, "/api/v1/tasks/"+id+"/approve", map[string]bool{"release": true},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	e.waitState(id, domain.StateFailed)
	got, _ := e.srv.st.GetTask(context.Background(), id)
	if strings.Contains(got.FailureReason+got.Error, "secret-internal-detail") {
		t.Fatalf("raw release error leaked into task: %q / %q", got.FailureReason, got.Error)
	}
}

func TestRejectAndCancel(t *testing.T) {
	rel := newFakeReleaser()
	e := newTaskEnv(t, rel)
	admin := e.session("root")
	e.makeProject("demo")

	review := e.createTask(admin, "demo", "task to reject")
	e.advanceTo(review, domain.StatePreparing, domain.StateRunning, domain.StateValidating, domain.StateReviewReady)
	rec := e.do(http.MethodPost, "/api/v1/tasks/"+review+"/reject", map[string]string{"reason": "not wanted"},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"CANCELLED"`) {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}

	queued := e.createTask(admin, "demo", "task to cancel")
	rec = e.do(http.MethodPost, "/api/v1/tasks/"+queued+"/cancel", nil, withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"CANCELLED"`) {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequestTriageAndTaskCreation(t *testing.T) {
	rel := newFakeReleaser()
	e := newTaskEnv(t, rel)
	admin := e.session("root")
	e.makeProject("demo")

	rec := e.do(http.MethodPost, "/api/v1/requests", map[string]any{
		"projectId": "demo", "type": "bug", "description": "crash on save",
	}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create request: %d %s", rec.Code, rec.Body.String())
	}
	var rq struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rq); err != nil || rq.Status != "new" {
		t.Fatalf("new request: %v %s", err, rec.Body.String())
	}

	rec = e.do(http.MethodPatch, "/api/v1/requests/"+rq.ID, map[string]any{"priority": 3},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("priority patch must be refused: %d", rec.Code)
	}
	rec = e.do(http.MethodPatch, "/api/v1/requests/"+rq.ID, map[string]any{"status": "triaged"},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("triage: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodPost, "/api/v1/requests/"+rq.ID+"/task", map[string]any{},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"state":"QUEUED"`) {
		t.Fatalf("request to task: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "crash on save") {
		t.Fatalf("prompt should carry the request description: %s", rec.Body.String())
	}

	rec = e.do(http.MethodGet, "/api/v1/requests/"+rq.ID, nil, withCookie(admin))
	if !strings.Contains(rec.Body.String(), `"status":"in_progress"`) || !strings.Contains(rec.Body.String(), `"task":{`) {
		t.Fatalf("request detail: %s", rec.Body.String())
	}

	rec = e.do(http.MethodPatch, "/api/v1/requests/"+rq.ID, map[string]any{"status": "triaged"},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusConflict {
		t.Fatalf("triage after task: %d %s", rec.Code, rec.Body.String())
	}
}

func TestProjectsListAndStats(t *testing.T) {
	rel := newFakeReleaser()
	e := newTaskEnv(t, rel)
	admin := e.session("root")
	e.makeProject("demo")
	e.makeProject("other")
	e.createTask(admin, "demo", "one active task")

	// Names are "Demo <id>", so "OTH" matches only the "other" project by name or ID.
	rec := e.do(http.MethodGet, "/api/v1/projects?q=OTH", nil, withCookie(admin))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"other"`) ||
		strings.Contains(rec.Body.String(), `"id":"demo"`) {
		t.Fatalf("project search: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodGet, "/api/v1/projects/demo", nil, withCookie(admin))
	var out struct {
		Stats struct {
			ActiveTasks int `json:"activeTasks"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Stats.ActiveTasks != 1 {
		t.Fatalf("project stats: %v %s", err, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/projects/missing", nil, withCookie(admin)); rec.Code != http.StatusNotFound {
		t.Fatalf("missing project: %d", rec.Code)
	}
}

func TestEventStreamDeliversTaskEvents(t *testing.T) {
	oldPoll := ssePollInterval
	ssePollInterval = 20 * time.Millisecond
	t.Cleanup(func() { ssePollInterval = oldPoll })

	rel := newFakeReleaser()
	e := newTaskEnv(t, rel)
	admin := e.session("root")
	e.makeProject("demo")

	// Replay from zero so events written before the connection are delivered.
	id := e.createTask(admin, "demo", "stream me")
	e.advanceTo(id, domain.StatePreparing)

	srv := httptest.NewServer(e.h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(admin)
	req.Header.Set("Last-Event-ID", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream headers: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	seen := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if name, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				seen <- name
			}
		}
		close(seen)
	}()

	want := map[string]bool{"system": false, "task_event": false, "task_updated": false}
	deadline := time.After(5 * time.Second)
	for {
		done := true
		for _, ok := range want {
			if !ok {
				done = false
			}
		}
		if done {
			break
		}
		select {
		case name, open := <-seen:
			if !open {
				t.Fatalf("stream closed early; seen %v", want)
			}
			if _, known := want[name]; known {
				want[name] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for stream events; seen %v", want)
		}
	}
}

func TestEventStreamRejectsBadLastEventID(t *testing.T) {
	e := newTaskEnv(t, newFakeReleaser())
	admin := e.session("root")
	rec := e.do(http.MethodGet, "/api/v1/events/stream", nil, withCookie(admin), func(r *http.Request) {
		r.Header.Set("Last-Event-ID", "not-a-number")
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad Last-Event-ID: %d %s", rec.Code, rec.Body.String())
	}
}

// itoa avoids pulling strconv into the test file for one call.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
