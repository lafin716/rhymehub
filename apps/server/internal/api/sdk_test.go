package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/redact"
	"github.com/lafin716/rhymehub/internal/store"
)

// newSDKEnv builds a server with the given data paths and a fresh admin. Tasks are not needed for most SDK tests.
func newSDKEnv(t *testing.T, paths config.Paths) *testEnv {
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
		Releaser: newFakeReleaser(), Canceller: &fakeCanceller{}, Paths: paths,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	e := &testEnv{t: t, h: srv.Handler(), srv: srv}
	e.setupAdmin("root")
	e.makeProject("demo")
	return e
}

// makeSDKKey creates a reporter key for projectID and returns the raw rhk_ value.
func (e *testEnv) makeSDKKey(projectID string, origins []string) string {
	e.t.Helper()
	raw := "rhk_" + strings.ToLower(domain.NewID("test"))
	if _, err := e.srv.st.CreateProjectKey(context.Background(), domain.ProjectKey{
		ProjectID: projectID, Name: "sdk", KeyHash: hashToken(raw), KeyPrefix: raw[:8], AllowedOrigins: origins,
	}); err != nil {
		e.t.Fatalf("create key: %v", err)
	}
	return raw
}

func withKey(raw string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(sdkKeyHeader, raw) }
}

func withIdem(v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(sdkIdempotencyHeader, v) }
}

func withOrigin(v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Origin", v) }
}

func sdkBody(inst, desc string) map[string]any {
	return map[string]any{"type": "bug", "description": desc, "installationId": inst, "appVersion": "1.2.3", "platform": "web"}
}

type sdkViewOut struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Question    string `json:"question"`
	Progress    struct {
		State string `json:"state"`
	} `json:"progress"`
}

func decodeSDKView(t *testing.T, rec *httptest.ResponseRecorder) sdkViewOut {
	t.Helper()
	var v sdkViewOut
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.ID == "" {
		t.Fatalf("decode sdk view: %v %s", err, rec.Body.String())
	}
	return v
}

func TestSDKRequiresReporterKey(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	if rec := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-1", "crash on start")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-1", "crash"), withKey("rhk_bogus")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key: %d", rec.Code)
	}
	// An admin session or bearer token must not reach the SDK routes.
	c := sessionCookieFrom(t, e.login("root", testPassword))
	if rec := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-1", "crash"), withCookie(c), withCSRF()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session on sdk route: %d", rec.Code)
	}
}

func TestSDKCreateAndGetIsScopedToInstallation(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	key := e.makeSDKKey("demo", nil)

	rec := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-A", "button does nothing"), withKey(key))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	v := decodeSDKView(t, rec)
	if strings.Contains(rec.Body.String(), "prompt") || strings.Contains(rec.Body.String(), "worktree") {
		t.Fatalf("sdk view leaks internal fields: %s", rec.Body.String())
	}

	if rec := e.do(http.MethodGet, "/api/v1/sdk/requests/"+v.ID+"?installationId=inst-A", nil, withKey(key)); rec.Code != http.StatusOK {
		t.Fatalf("get own: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/sdk/requests/"+v.ID+"?installationId=inst-B", nil, withKey(key)); rec.Code != http.StatusNotFound {
		t.Fatalf("get other installation: %d", rec.Code)
	}

	rec = e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-B", nil, withKey(key))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), v.ID) {
		t.Fatalf("list leaked other installation: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-A", nil, withKey(key))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), v.ID) {
		t.Fatalf("list own: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSDKStoresRedactedDescription(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	key := e.makeSDKKey("demo", nil)
	raw := "crash with token ghp_0123456789abcdefghijklmnopqrstuvwxyz1234 in logs"
	want, _ := redact.Redact(raw)

	rec := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-A", raw), withKey(key))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeSDKView(t, rec).Description; got != want {
		t.Fatalf("description = %q, want redacted %q", got, want)
	}
}

func TestSDKIdempotencyKey(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	key := e.makeSDKKey("demo", nil)

	first := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-A", "same report"), withKey(key), withIdem("idem-1"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	id := decodeSDKView(t, first).ID

	replay := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-A", "same report"), withKey(key), withIdem("idem-1"))
	if replay.Code != http.StatusOK || decodeSDKView(t, replay).ID != id {
		t.Fatalf("replay: %d %s, want original %s", replay.Code, replay.Body.String(), id)
	}

	other := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-A", "different body"), withKey(key), withIdem("idem-1"))
	if other.Code != http.StatusConflict || decodeErrCode(t, other) != "conflict" {
		t.Fatalf("same key different body: %d %s", other.Code, other.Body.String())
	}
}

func TestSDKBodyCap(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	key := e.makeSDKKey("demo", nil)
	big := strings.Repeat("a", sdkMaxBodyBytes+1024)
	rec := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-A", big), withKey(key))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d", rec.Code)
	}
}

func TestSDKRateLimitPerInstallation(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	key := e.makeSDKKey("demo", nil)
	for i := 0; i < sdkRatePerMinute; i++ {
		if rec := e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-A", nil, withKey(key)); rec.Code != http.StatusOK {
			t.Fatalf("call %d: %d", i, rec.Code)
		}
	}
	rec := e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-A", nil, withKey(key))
	if rec.Code != http.StatusTooManyRequests || decodeErrCode(t, rec) != "rate_limited" {
		t.Fatalf("over limit: %d %s", rec.Code, rec.Body.String())
	}
	// A different installation under the same key has its own bucket.
	if rec := e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-B", nil, withKey(key)); rec.Code != http.StatusOK {
		t.Fatalf("other installation: %d", rec.Code)
	}
}

func TestSDKAnswerOnlyWhileWaitingInput(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	key := e.makeSDKKey("demo", nil)
	admin := e.session("root")

	created := e.do(http.MethodPost, "/api/v1/sdk/requests", sdkBody("inst-A", "needs a question"), withKey(key))
	reqID := decodeSDKView(t, created).ID

	// No task yet: answering is a conflict.
	answer := map[string]string{"installationId": "inst-A", "text": "use the staging API"}
	if rec := e.do(http.MethodPost, "/api/v1/sdk/requests/"+reqID+"/answer", answer, withKey(key)); rec.Code != http.StatusConflict {
		t.Fatalf("answer before task: %d %s", rec.Code, rec.Body.String())
	}

	rec := e.do(http.MethodPost, "/api/v1/requests/"+reqID+"/task", map[string]any{}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rec.Code, rec.Body.String())
	}
	var task struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil || task.ID == "" {
		t.Fatalf("decode task: %v %s", err, rec.Body.String())
	}
	e.advanceTo(task.ID, domain.StatePreparing, domain.StateRunning, domain.StateWaitingInput)
	if _, err := e.srv.st.MutateTask(context.Background(), task.ID, func(t *domain.Task) error {
		t.PendingQuestion = "Which environment?"
		return nil
	}); err != nil {
		t.Fatalf("set question: %v", err)
	}

	// The question is shown to the reporter.
	if got := e.do(http.MethodGet, "/api/v1/sdk/requests/"+reqID+"?installationId=inst-A", nil, withKey(key)); decodeSDKView(t, got).Question != "Which environment?" {
		t.Fatalf("question not exposed: %s", got.Body.String())
	}

	// Another installation cannot answer.
	other := map[string]string{"installationId": "inst-B", "text": "nope"}
	if rec := e.do(http.MethodPost, "/api/v1/sdk/requests/"+reqID+"/answer", other, withKey(key)); rec.Code != http.StatusNotFound {
		t.Fatalf("other installation answer: %d", rec.Code)
	}

	if rec := e.do(http.MethodPost, "/api/v1/sdk/requests/"+reqID+"/answer", answer, withKey(key)); rec.Code != http.StatusOK {
		t.Fatalf("answer while waiting: %d %s", rec.Code, rec.Body.String())
	}
	got, err := e.srv.st.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.State != domain.StateQueued || got.NextInput != "use the staging API" || got.PendingQuestion != "" {
		t.Fatalf("task after answer: state=%s next=%q question=%q", got.State, got.NextInput, got.PendingQuestion)
	}

	// The task is no longer waiting, so a second answer is refused.
	if rec := e.do(http.MethodPost, "/api/v1/sdk/requests/"+reqID+"/answer", answer, withKey(key)); rec.Code != http.StatusConflict {
		t.Fatalf("second answer: %d", rec.Code)
	}
}

func TestSDKCORSPreflightAndOriginPolicy(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	restricted := e.makeSDKKey("demo", []string{"https://app.example"})

	pre := e.do(http.MethodOptions, "/api/v1/sdk/requests", nil, withOrigin("https://app.example"))
	if pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Origin") != "https://app.example" {
		t.Fatalf("preflight: %d headers=%v", pre.Code, pre.Header())
	}

	if rec := e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-A", nil, withKey(restricted), withOrigin("https://evil.example")); rec.Code != http.StatusForbidden {
		t.Fatalf("disallowed origin: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-A", nil, withKey(restricted), withOrigin("https://app.example")); rec.Code != http.StatusOK {
		t.Fatalf("allowed origin: %d", rec.Code)
	}
}

func TestSDKRevokedKeyIsRejected(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	raw := e.makeSDKKey("demo", nil)
	ctx := context.Background()
	k, err := e.srv.st.GetProjectKeyByHash(ctx, hashToken(raw))
	if err != nil {
		t.Fatalf("lookup key: %v", err)
	}
	if err := e.srv.st.RevokeProjectKey(ctx, k.ID, e.srv.now()); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if rec := e.do(http.MethodGet, "/api/v1/sdk/requests?installationId=inst-A", nil, withKey(raw)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key: %d", rec.Code)
	}
}

func TestSDKAttachmentsNeedDataDirectory(t *testing.T) {
	logBody := sdkBody("inst-A", "with a log")
	logBody["attachments"] = []map[string]string{{
		"kind": "log", "filename": "app.log", "mime": "text/plain",
		"dataBase64": base64.StdEncoding.EncodeToString([]byte("boom")),
	}}

	none := newSDKEnv(t, config.Paths{})
	if rec := none.do(http.MethodPost, "/api/v1/sdk/requests", logBody, withKey(none.makeSDKKey("demo", nil))); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("attachments without data dir: %d %s", rec.Code, rec.Body.String())
	}

	root := t.TempDir()
	e := newSDKEnv(t, config.Paths{Root: root})
	rec := e.do(http.MethodPost, "/api/v1/sdk/requests", logBody, withKey(e.makeSDKKey("demo", nil)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("attachment create: %d %s", rec.Code, rec.Body.String())
	}
	entries, err := os.ReadDir(filepath.Join(root, "attachments"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("attachment files: %d %v", len(entries), err)
	}
	info, err := entries[0].Info()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("attachment mode: %v %v", info.Mode(), err)
	}
}

func TestSDKLatestReleaseWithoutReleases(t *testing.T) {
	e := newSDKEnv(t, config.Paths{})
	key := e.makeSDKKey("demo", nil)
	rec := e.do(http.MethodGet, "/api/v1/sdk/releases/latest?currentVersion=1.0.0&platform=web", nil, withKey(key))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"updateAvailable":false`) ||
		!strings.Contains(rec.Body.String(), `"kind":"none"`) {
		t.Fatalf("latest release: %d %s", rec.Code, rec.Body.String())
	}
}
