package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
)

const testSupToken = "sup-secret-token"

// fakeSched implements SchedulerControl.
type fakeSched struct {
	mu       sync.Mutex
	draining bool
	active   int
	limits   AgentLimits
}

func (f *fakeSched) Drain(on bool) { f.mu.Lock(); f.draining = on; f.mu.Unlock() }
func (f *fakeSched) Draining() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.draining
}
func (f *fakeSched) ActiveWorkers() int { return f.active }
func (f *fakeSched) CurrentLimits() AgentLimits {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.limits
}
func (f *fakeSched) SetLimits(l AgentLimits) { f.mu.Lock(); f.limits = l; f.mu.Unlock() }

// fakeAgents implements AgentDirectory.
type fakeAgents struct{ infos []AgentInfo }

func (f fakeAgents) ListAgents(context.Context) []AgentInfo { return f.infos }

// fakeSup implements Supervisor and SupervisorInfo.
type fakeSup struct {
	status  SupervisorStatus
	history []json.RawMessage
	err     error
	updates chan string
	rolls   chan string
}

func newFakeSup(status SupervisorStatus, history ...json.RawMessage) *fakeSup {
	return &fakeSup{status: status, history: history, updates: make(chan string, 4), rolls: make(chan string, 4)}
}

func (f *fakeSup) Update(_ context.Context, v string) error    { f.updates <- v; return nil }
func (f *fakeSup) Rollback(_ context.Context, id string) error { f.rolls <- id; return nil }
func (f *fakeSup) Status(context.Context) SupervisorStatus     { return f.status }
func (f *fakeSup) UpdateHistory(context.Context) ([]json.RawMessage, error) {
	return f.history, f.err
}

// fakeNotifier implements Notifier.
type fakeNotifier struct{ key string }

func (f fakeNotifier) PublicKey() string { return f.key }
func (f fakeNotifier) Enqueue(_ context.Context, kind, eventKey, title, body, url, projectID, taskID string) (domain.Notification, bool, error) {
	return domain.Notification{ID: "n-test", Kind: kind, Title: title}, true, nil
}

// newWiredEnv builds a server on the same temp store as newTestEnv, with the given collaborators.
func newWiredEnv(t *testing.T, mod func(*Options)) *testEnv {
	t.Helper()
	base := newTestEnv(t, config.Default())
	opts := Options{
		Store:   base.srv.st,
		Config:  config.Default(),
		Version: "test",
		Paths:   config.NewPaths(t.TempDir()),
	}
	mod(&opts)
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return &testEnv{t: t, h: srv.Handler(), srv: srv}
}

// roleCookies creates an admin and a developer and returns their session cookies.
func roleCookies(t *testing.T, e *testEnv) (admin, dev *http.Cookie) {
	t.Helper()
	e.setupAdmin("root")
	admin = sessionCookieFrom(t, e.login("root", testPassword))
	rec := e.do(http.MethodPost, "/api/v1/users",
		map[string]string{"username": "dev", "password": testPassword, "role": "developer"},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create developer: %d %s", rec.Code, rec.Body.String())
	}
	dev = sessionCookieFrom(t, e.login("dev", testPassword))
	return admin, dev
}

func TestDrainRequiresSupervisorToken(t *testing.T) {
	sched := &fakeSched{}
	e := newWiredEnv(t, func(o *Options) {
		o.SupervisorToken = testSupToken
		o.Scheduler = sched
	})
	admin, _ := roleCookies(t, e)
	_ = admin

	cases := map[string]func(*http.Request){
		"no header":   func(*http.Request) {},
		"wrong token": withBearer("not-the-token"),
		"user bearer": withBearer("rht_someusertoken"),
		"empty token": withBearer(""),
	}
	for name, opt := range cases {
		rec := e.do(http.MethodPost, "/api/v1/system/drain", map[string]bool{"drain": true}, opt)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, want 401 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	if sched.Draining() || e.srv.Draining() {
		t.Fatal("rejected drain must not change drain state")
	}

	rec := e.do(http.MethodPost, "/api/v1/system/drain", map[string]bool{"drain": true}, withBearer(testSupToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("correct token: %d %s", rec.Code, rec.Body.String())
	}
	if !sched.Draining() || !e.srv.Draining() {
		t.Fatal("drain was not applied to scheduler and server")
	}
	if !strings.Contains(rec.Body.String(), `"draining":true`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestDrainWithUnconfiguredTokenAlwaysRejected(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) {})
	for _, h := range []string{"Bearer ", "Bearer x", ""} {
		rec := e.do(http.MethodPost, "/api/v1/system/drain", map[string]bool{"drain": false}, func(r *http.Request) {
			if h != "" {
				r.Header.Set("Authorization", h)
			}
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("header %q: status %d, want 401", h, rec.Code)
		}
	}
}

func TestSystemStatusNilSupervisor(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) {})
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodGet, "/api/v1/system/status", nil, withCookie(admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Version    string `json:"version"`
		Home       string `json:"home"`
		Supervisor struct {
			Reachable bool `json:"reachable"`
		} `json:"supervisor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != "test" || body.Home == "" || body.Supervisor.Reachable {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestSystemStatusWithFakeSupervisor(t *testing.T) {
	sup := newFakeSup(SupervisorStatus{Reachable: true, Version: "0.2.0", CurrentVersion: "0.2.0", UpdateInProgress: true})
	e := newWiredEnv(t, func(o *Options) { o.Supervisor = sup })
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodGet, "/api/v1/system/status", nil, withCookie(admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"reachable":true`, `"currentVersion":"0.2.0"`, `"updateInProgress":true`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, rec.Body.String())
		}
	}
}

func TestSystemUpdatesReturnsHistory(t *testing.T) {
	rec0 := json.RawMessage(`{"id":"upd_1","from":"0.1.0","to":"0.2.0","status":"succeeded"}`)
	sup := newFakeSup(SupervisorStatus{Reachable: true}, rec0)
	e := newWiredEnv(t, func(o *Options) { o.Supervisor = sup })
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodGet, "/api/v1/system/updates", nil, withCookie(admin))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"upd_1"`) {
		t.Fatalf("updates: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"available":[]`) {
		t.Fatalf("available must be an empty list: %s", rec.Body.String())
	}
}

func TestSystemUpdateRoleAndValidation(t *testing.T) {
	sup := newFakeSup(SupervisorStatus{Reachable: true})
	e := newWiredEnv(t, func(o *Options) { o.Supervisor = sup })
	admin, dev := roleCookies(t, e)

	rec := e.do(http.MethodPost, "/api/v1/system/update", map[string]string{"version": "0.3.0"},
		withCookie(dev), withCSRF())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer update: %d", rec.Code)
	}
	for _, bad := range []string{"", "-1.0.0", "latest", "1.0.0; rm -rf /"} {
		rec = e.do(http.MethodPost, "/api/v1/system/update", map[string]string{"version": bad},
			withCookie(admin), withCSRF())
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("version %q: %d", bad, rec.Code)
		}
	}
	rec = e.do(http.MethodPost, "/api/v1/system/update", map[string]string{"version": "0.3.0"},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"accepted":true`) {
		t.Fatalf("admin update: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case v := <-sup.updates:
		if v != "0.3.0" {
			t.Fatalf("forwarded version %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("update was not forwarded to the supervisor")
	}
}

func TestSystemUpdateUnavailableAndBusy(t *testing.T) {
	admin, _ := func() (*http.Cookie, *http.Cookie) {
		e := newWiredEnv(t, func(o *Options) {})
		return roleCookies(t, e)
	}()
	_ = admin

	// nil supervisor: 503.
	e := newWiredEnv(t, func(o *Options) {})
	adm, _ := roleCookies(t, e)
	rec := e.do(http.MethodPost, "/api/v1/system/update", map[string]string{"version": "0.3.0"}, withCookie(adm), withCSRF())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil supervisor: %d", rec.Code)
	}
	rec = e.do(http.MethodPost, "/api/v1/system/rollback", map[string]string{"updateId": "upd_1"}, withCookie(adm), withCSRF())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil supervisor rollback: %d", rec.Code)
	}

	// unreachable supervisor: 503.
	down := newFakeSup(SupervisorStatus{Reachable: false})
	e = newWiredEnv(t, func(o *Options) { o.Supervisor = down })
	adm, _ = roleCookies(t, e)
	rec = e.do(http.MethodPost, "/api/v1/system/update", map[string]string{"version": "0.3.0"}, withCookie(adm), withCSRF())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable: %d", rec.Code)
	}

	// update already in progress: 409.
	busy := newFakeSup(SupervisorStatus{Reachable: true, UpdateInProgress: true})
	e = newWiredEnv(t, func(o *Options) { o.Supervisor = busy })
	adm, _ = roleCookies(t, e)
	rec = e.do(http.MethodPost, "/api/v1/system/update", map[string]string{"version": "0.3.0"}, withCookie(adm), withCSRF())
	if rec.Code != http.StatusConflict {
		t.Fatalf("busy: %d", rec.Code)
	}
}

func TestSystemRollbackChecksHistoryAndRole(t *testing.T) {
	rec0 := json.RawMessage(`{"id":"upd_1","from":"0.1.0","to":"0.2.0","status":"succeeded"}`)
	sup := newFakeSup(SupervisorStatus{Reachable: true}, rec0)
	e := newWiredEnv(t, func(o *Options) { o.Supervisor = sup })
	admin, dev := roleCookies(t, e)

	rec := e.do(http.MethodPost, "/api/v1/system/rollback", map[string]string{"updateId": "upd_1"}, withCookie(dev), withCSRF())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer rollback: %d", rec.Code)
	}
	rec = e.do(http.MethodPost, "/api/v1/system/rollback", map[string]string{"updateId": "upd_missing"}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/api/v1/system/rollback", map[string]string{"updateId": "upd_1"}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case id := <-sup.rolls:
		if id != "upd_1" {
			t.Fatalf("rolled back %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rollback was not forwarded")
	}
}

func TestAgentsListWithFakeDirectory(t *testing.T) {
	sched := &fakeSched{limits: AgentLimits{Global: 4, DefaultPerAgent: 2, PerAgent: map[string]int{"codex": 1}}}
	agents := fakeAgents{infos: []AgentInfo{
		{Name: "claude", Available: true, Version: "1.0", Path: "/bin/claude", Capabilities: AgentCapabilities{Streaming: true}},
		{Name: "codex", Available: false, Error: "not installed"},
	}}
	e := newWiredEnv(t, func(o *Options) {
		o.Scheduler = sched
		o.Agents = agents
	})
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodGet, "/api/v1/agents", nil, withCookie(admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("agents: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Agents []struct {
			Name    string `json:"name"`
			Running int    `json:"running"`
			Limit   int    `json:"limit"`
			Error   string `json:"error"`
		} `json:"agents"`
		Limits AgentLimits `json:"limits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Agents) != 2 {
		t.Fatalf("agents = %+v", body.Agents)
	}
	if body.Agents[0].Limit != 2 || body.Agents[1].Limit != 1 || body.Agents[1].Error != "not installed" {
		t.Fatalf("agents = %+v", body.Agents)
	}
	if body.Limits.Global != 4 {
		t.Fatalf("limits = %+v", body.Limits)
	}
}

func TestAgentsListNilCollaborators(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) {})
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodGet, "/api/v1/agents", nil, withCookie(admin))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"agents":[]`) {
		t.Fatalf("nil agents: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPatchAgentLimitsAdminOnlyAndPersisted(t *testing.T) {
	sched := &fakeSched{limits: AgentLimits{Global: 1}}
	e := newWiredEnv(t, func(o *Options) { o.Scheduler = sched })
	admin, dev := roleCookies(t, e)

	patch := map[string]any{"global": 4, "perAgent": map[string]int{"claude": 2}}
	rec := e.do(http.MethodPatch, "/api/v1/agents/limits", patch, withCookie(dev), withCSRF())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer patch: %d", rec.Code)
	}
	rec = e.do(http.MethodPatch, "/api/v1/agents/limits", map[string]int{"global": -1}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative limit: %d", rec.Code)
	}
	rec = e.do(http.MethodPatch, "/api/v1/agents/limits", patch, withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("admin patch: %d %s", rec.Code, rec.Body.String())
	}
	got := sched.CurrentLimits()
	if got.Global != 4 || got.PerAgent["claude"] != 2 {
		t.Fatalf("scheduler limits = %+v", got)
	}
	raw, err := e.srv.st.GetSetting(context.Background(), settingSchedulerLimits)
	if err != nil {
		t.Fatalf("setting not persisted: %v", err)
	}
	var persisted AgentLimits
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		t.Fatalf("persisted value %q: %v", raw, err)
	}
	if persisted.Global != 4 || persisted.PerAgent["claude"] != 2 {
		t.Fatalf("persisted = %+v", persisted)
	}
}

func TestPatchAgentLimitsWithoutSchedulerIs503(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) {})
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodPatch, "/api/v1/agents/limits", map[string]int{"global": 1}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no scheduler: %d", rec.Code)
	}
}

func TestVAPIDKeyAndNilNotifier(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) {})
	admin, _ := roleCookies(t, e)
	rec := e.do(http.MethodGet, "/api/v1/push/vapid-public-key", nil, withCookie(admin))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil notifier: %d", rec.Code)
	}
	e = newWiredEnv(t, func(o *Options) { o.Notifier = fakeNotifier{key: "BPUB"} })
	admin, _ = roleCookies(t, e)
	rec = e.do(http.MethodGet, "/api/v1/push/vapid-public-key", nil, withCookie(admin))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"key":"BPUB"`) {
		t.Fatalf("vapid: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDrainRejectsMissingDrainField(t *testing.T) {
	e := newWiredEnv(t, func(o *Options) { o.SupervisorToken = testSupToken })
	rec := e.do(http.MethodPost, "/api/v1/system/drain", map[string]any{}, withBearer(testSupToken))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing drain: %d", rec.Code)
	}
	_ = errors.New // keep errors imported for future negative cases
}
