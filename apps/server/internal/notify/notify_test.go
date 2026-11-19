package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
)

// fakeStore is an in-memory Store.
type fakeStore struct {
	mu    sync.Mutex
	seq   int
	notes map[string]domain.Notification
	keys  map[string]string
	subs  []domain.PushSubscription
}

func newFakeStore() *fakeStore {
	return &fakeStore{notes: map[string]domain.Notification{}, keys: map[string]string{}}
}

func (f *fakeStore) EnqueueNotification(_ context.Context, n domain.Notification) (domain.Notification, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.keys[n.EventKey]; ok {
		return f.notes[id], false, nil
	}
	f.seq++
	n.ID = fmt.Sprintf("ntf_%d", f.seq)
	if n.Status == "" {
		n.Status = domain.NotifyPending
	}
	f.notes[n.ID] = n
	f.keys[n.EventKey] = n.ID
	return n, true, nil
}

func (f *fakeStore) ListDueNotifications(_ context.Context, now time.Time, limit int) ([]domain.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Notification
	for i := 1; i <= f.seq; i++ {
		n, ok := f.notes[fmt.Sprintf("ntf_%d", i)]
		if !ok || n.Status != domain.NotifyPending || n.NextAttemptAt.After(now) {
			continue
		}
		out = append(out, n)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) MarkNotification(_ context.Context, id string, status domain.NotifyStatus, attempts int, lastError string, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.notes[id]
	if !ok {
		return domain.ErrNotFound
	}
	n.Status, n.Attempts, n.LastError, n.NextAttemptAt = status, attempts, lastError, next
	f.notes[id] = n
	return nil
}

func (f *fakeStore) ListAllPushSubscriptions(_ context.Context) ([]domain.PushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.PushSubscription(nil), f.subs...), nil
}

func (f *fakeStore) DeletePushSubscription(_ context.Context, endpoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []domain.PushSubscription
	for _, s := range f.subs {
		if s.Endpoint != endpoint {
			kept = append(kept, s)
		}
	}
	f.subs = kept
	return nil
}

func (f *fakeStore) note(id string) domain.Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notes[id]
}

func (f *fakeStore) subCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// validSub returns a subscription with real P-256 and auth keys so the RFC 8291 encryption succeeds.
func validSub(t *testing.T, endpoint string) domain.PushSubscription {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("auth secret: %v", err)
	}
	return domain.PushSubscription{
		UserID:   "usr_1",
		Endpoint: endpoint,
		P256DH:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}
}

type pushServer struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	requests int
	bodies   [][]byte
}

func newPushServer(t *testing.T, status int) *pushServer {
	t.Helper()
	ps := &pushServer{status: status}
	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.requests++
		ps.bodies = append(ps.bodies, body)
		code := ps.status
		ps.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid ") && !strings.HasPrefix(r.Header.Get("Authorization"), "WebPush ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(ps.Close)
	return ps
}

func (ps *pushServer) setStatus(code int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.status = code
}

func (ps *pushServer) count() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.requests
}

type testEnv struct {
	svc   *Service
	store *fakeStore
	clk   *clock
	paths config.Paths
}

// newEnv builds a Service over a temp RHYMEHUB_HOME. It never touches the real ~/.rhymehub.
func newEnv(t *testing.T, maxAttempts int) testEnv {
	t.Helper()
	root := t.TempDir()
	t.Setenv(config.EnvHome, root)
	paths := config.NewPaths(root)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	st := newFakeStore()
	clk := &clock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	svc, err := New(Options{
		Store:       st,
		Paths:       paths,
		Now:         clk.now,
		MaxAttempts: maxAttempts,
		Tick:        10 * time.Millisecond,
		allowHTTP:   true,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return testEnv{svc: svc, store: st, clk: clk, paths: paths}
}

func TestVAPIDKeysGeneratedOnceWith0600(t *testing.T) {
	env := newEnv(t, 0)
	keyFile := filepath.Join(env.paths.SecretsDir(), vapidFile)
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("stat vapid file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("vapid file mode = %o, want 600", mode)
	}
	if env.svc.PublicKey() == "" {
		t.Fatal("public key is empty")
	}

	// A second service over the same home loads the same key pair instead of regenerating it.
	again, err := New(Options{Store: newFakeStore(), Paths: env.paths})
	if err != nil {
		t.Fatalf("reload service: %v", err)
	}
	if again.PublicKey() != env.svc.PublicKey() {
		t.Fatal("reload generated a new VAPID key pair")
	}
}

func TestVAPIDRejectsIncompleteFile(t *testing.T) {
	root := t.TempDir()
	paths := config.NewPaths(root)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.SecretsDir(), vapidFile), []byte(`{"publicKey":"x"}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := New(Options{Store: newFakeStore(), Paths: paths}); err == nil {
		t.Fatal("incomplete vapid file must be rejected")
	}
}

func TestEnqueueDedupesAndFansOut(t *testing.T) {
	env := newEnv(t, 0)
	ctx := context.Background()
	ch := make(chan domain.Notification, 4)
	unsub := env.svc.Subscribe(ch)

	n, inserted, err := env.svc.Enqueue(ctx, KindReviewReady, "task:tsk_1:REVIEW_READY:3", "Ready for review",
		"fix login", "/tasks/tsk_1", "prj_1", "tsk_1")
	if err != nil || !inserted {
		t.Fatalf("first enqueue: inserted=%v err=%v", inserted, err)
	}
	if n.Status != domain.NotifyPending || n.Kind != KindReviewReady {
		t.Fatalf("saved notification = %+v", n)
	}
	select {
	case got := <-ch:
		if got.ID != n.ID {
			t.Fatalf("fan-out id = %s, want %s", got.ID, n.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive the notification")
	}

	again, inserted, err := env.svc.Enqueue(ctx, KindReviewReady, "task:tsk_1:REVIEW_READY:3", "dup", "", "", "", "")
	if err != nil || inserted || again.ID != n.ID {
		t.Fatalf("dedupe: inserted=%v id=%s err=%v", inserted, again.ID, err)
	}
	select {
	case got := <-ch:
		t.Fatalf("duplicate was fanned out: %+v", got)
	default:
	}

	unsub()
	if _, _, err := env.svc.Enqueue(ctx, KindWaitingInput, "task:tsk_1:WAITING_INPUT:4", "t", "", "", "", ""); err != nil {
		t.Fatalf("enqueue after unsubscribe: %v", err)
	}
	select {
	case got := <-ch:
		t.Fatalf("unsubscribed channel received %+v", got)
	default:
	}
}

func TestEnqueueValidation(t *testing.T) {
	env := newEnv(t, 0)
	ctx := context.Background()
	if _, _, err := env.svc.Enqueue(ctx, "", "k", "t", "", "", "", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty kind err = %v", err)
	}
	if _, _, err := env.svc.Enqueue(ctx, KindReviewReady, "", "t", "", "", "", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty event key err = %v", err)
	}
	for _, bad := range []string{"https://evil.example/x", "//evil.example/x", "tasks/1"} {
		if _, _, err := env.svc.Enqueue(ctx, KindReviewReady, "k:"+bad, "t", "", bad, "", ""); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("url %q err = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestEnqueueClipsPayloadText(t *testing.T) {
	env := newEnv(t, 0)
	longTitle := strings.Repeat("가", maxTitleRunes+50)
	longBody := strings.Repeat("나", maxBodyRunes+50)
	n, _, err := env.svc.Enqueue(context.Background(), KindReviewReady, "k1", longTitle, longBody, "", "", "")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := len([]rune(n.Title)); got != maxTitleRunes {
		t.Fatalf("title runes = %d, want %d", got, maxTitleRunes)
	}
	if got := len([]rune(n.Body)); got != maxBodyRunes {
		t.Fatalf("body runes = %d, want %d", got, maxBodyRunes)
	}
}

func TestDeliverSendsEncryptedPushAndMarksSent(t *testing.T) {
	env := newEnv(t, 0)
	ctx := context.Background()
	push := newPushServer(t, http.StatusCreated)
	env.store.subs = []domain.PushSubscription{validSub(t, push.URL+"/push/1")}
	n, _, err := env.svc.Enqueue(ctx, KindReviewReady, "task:tsk_9:REVIEW_READY:1", "Ready for review", "fix login", "/tasks/tsk_9", "prj_1", "tsk_9")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	done, err := env.svc.DeliverDue(ctx)
	if err != nil || done != 1 {
		t.Fatalf("deliver: done=%d err=%v", done, err)
	}
	if push.count() != 1 {
		t.Fatalf("push requests = %d, want 1", push.count())
	}
	got := env.store.note(n.ID)
	if got.Status != domain.NotifySent || got.Attempts != 1 || got.LastError != "" {
		t.Fatalf("after delivery = %+v", got)
	}
	// The body is encrypted, so the plaintext must not be visible on the wire.
	if strings.Contains(string(push.bodies[0]), "fix login") {
		t.Fatal("plaintext payload leaked on the wire")
	}
	// Sent notifications are not due again.
	if done, _ := env.svc.DeliverDue(ctx); done != 0 {
		t.Fatalf("second pass processed %d notifications", done)
	}
}

func TestDeliverDeletesGoneSubscriptions(t *testing.T) {
	env := newEnv(t, 0)
	ctx := context.Background()
	push := newPushServer(t, http.StatusGone)
	env.store.subs = []domain.PushSubscription{validSub(t, push.URL+"/gone")}
	n, _, _ := env.svc.Enqueue(ctx, KindWaitingInput, "k-gone", "t", "", "", "", "")

	if _, err := env.svc.DeliverDue(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if env.store.subCount() != 0 {
		t.Fatal("410 subscription was not deleted")
	}
	got := env.store.note(n.ID)
	if got.Status != domain.NotifySkipped {
		t.Fatalf("status = %s, want skipped when no live subscription remains", got.Status)
	}
}

func TestDeliverNotFoundAlsoDeletesSubscription(t *testing.T) {
	env := newEnv(t, 0)
	ctx := context.Background()
	push := newPushServer(t, http.StatusNotFound)
	env.store.subs = []domain.PushSubscription{validSub(t, push.URL+"/missing")}
	env.svc.Enqueue(ctx, KindWaitingInput, "k-404", "t", "", "", "", "")
	if _, err := env.svc.DeliverDue(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if env.store.subCount() != 0 {
		t.Fatal("404 subscription was not deleted")
	}
}

func TestDeliverRetriesWithBackoffThenFails(t *testing.T) {
	env := newEnv(t, 2)
	ctx := context.Background()
	push := newPushServer(t, http.StatusInternalServerError)
	env.store.subs = []domain.PushSubscription{validSub(t, push.URL+"/flaky")}
	n, _, _ := env.svc.Enqueue(ctx, KindFailedTests, "k-retry", "Tests failed", "", "", "", "")

	// Attempt 1 fails transiently and is rescheduled 30 s later.
	if _, err := env.svc.DeliverDue(ctx); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	got := env.store.note(n.ID)
	if got.Status != domain.NotifyPending || got.Attempts != 1 {
		t.Fatalf("after first failure = %+v", got)
	}
	if want := env.clk.now().Add(30 * time.Second); !got.NextAttemptAt.Equal(want) {
		t.Fatalf("next attempt = %s, want %s", got.NextAttemptAt, want)
	}
	if got.LastError == "" || strings.Contains(got.LastError, push.URL) {
		t.Fatalf("last error = %q, must describe the failure without the endpoint", got.LastError)
	}
	if done, _ := env.svc.DeliverDue(ctx); done != 0 {
		t.Fatal("notification delivered before its backoff elapsed")
	}

	// Attempt 2 reaches MaxAttempts and the notification is marked failed.
	env.clk.advance(time.Minute)
	if _, err := env.svc.DeliverDue(ctx); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	got = env.store.note(n.ID)
	if got.Status != domain.NotifyFailed || got.Attempts != 2 {
		t.Fatalf("after max attempts = %+v", got)
	}
	if push.count() != 2 {
		t.Fatalf("push requests = %d, want 2", push.count())
	}
}

func TestDeliverRecoversAfterTransientFailure(t *testing.T) {
	env := newEnv(t, 0)
	ctx := context.Background()
	push := newPushServer(t, http.StatusServiceUnavailable)
	env.store.subs = []domain.PushSubscription{validSub(t, push.URL+"/recover")}
	n, _, _ := env.svc.Enqueue(ctx, KindReviewReady, "k-recover", "Ready", "", "", "", "")
	if _, err := env.svc.DeliverDue(ctx); err != nil {
		t.Fatalf("pass 1: %v", err)
	}
	push.setStatus(http.StatusCreated)
	env.clk.advance(time.Minute)
	if _, err := env.svc.DeliverDue(ctx); err != nil {
		t.Fatalf("pass 2: %v", err)
	}
	if got := env.store.note(n.ID); got.Status != domain.NotifySent || got.Attempts != 2 {
		t.Fatalf("after recovery = %+v", got)
	}
}

func TestDeliverWithoutSubscriptionsIsSkipped(t *testing.T) {
	env := newEnv(t, 0)
	ctx := context.Background()
	n, _, _ := env.svc.Enqueue(ctx, KindReviewReady, "k-none", "Ready", "", "", "", "")
	if _, err := env.svc.DeliverDue(ctx); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := env.store.note(n.ID); got.Status != domain.NotifySkipped {
		t.Fatalf("status = %s, want skipped", got.Status)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	env := newEnv(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- env.svc.Run(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestValidateEndpoint(t *testing.T) {
	cases := []struct {
		endpoint  string
		allowHTTP bool
		ok        bool
	}{
		{"https://push.example.com/abc", false, true},
		{"http://push.example.com/abc", false, false},
		{"http://127.0.0.1:9/abc", true, true},
		{"file:///etc/passwd", true, false},
		{"not a url", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		err := validateEndpoint(c.endpoint, c.allowHTTP)
		if (err == nil) != c.ok {
			t.Fatalf("validateEndpoint(%q, %v) = %v, want ok=%v", c.endpoint, c.allowHTTP, err, c.ok)
		}
		if err != nil && !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("validateEndpoint(%q) error %v must wrap ErrInvalid", c.endpoint, err)
		}
	}
}

func TestBackoffCapsAtOneHour(t *testing.T) {
	if got := backoff(1); got != 30*time.Second {
		t.Fatalf("backoff(1) = %s", got)
	}
	if got := backoff(2); got != time.Minute {
		t.Fatalf("backoff(2) = %s", got)
	}
	if got := backoff(20); got != time.Hour {
		t.Fatalf("backoff(20) = %s, want 1h cap", got)
	}
}

func TestPayloadHasOnlyDisplayFields(t *testing.T) {
	msg := newMessage(domain.Notification{
		ID: "ntf_1", EventKey: "task:tsk_1:REVIEW_READY:2", Kind: KindReviewReady,
		Title: "Ready", Body: "fix", URL: "/tasks/tsk_1", LastError: "secret-ish",
	})
	var fields map[string]any
	if err := json.Unmarshal(msg, &fields); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	want := map[string]bool{"id": true, "kind": true, "title": true, "body": true, "url": true}
	if len(fields) != len(want) {
		t.Fatalf("payload fields = %v", fields)
	}
	for k := range fields {
		if !want[k] {
			t.Fatalf("unexpected payload field %q", k)
		}
	}
	if strings.Contains(string(msg), "task:tsk_1") || strings.Contains(string(msg), "secret-ish") {
		t.Fatalf("payload leaks internal fields: %s", msg)
	}
}

func TestForTaskTransitionKinds(t *testing.T) {
	task := domain.Task{ID: "tsk_1", ProjectID: "prj_1", Title: "Fix login", Version: 7}
	cases := []struct {
		to      domain.TaskState
		reason  string
		kind    string
		notify  bool
		keyWant string
	}{
		{domain.StateReviewReady, "", KindReviewReady, true, "task:tsk_1:REVIEW_READY:7"},
		{domain.StateWaitingInput, "", KindWaitingInput, true, "task:tsk_1:WAITING_INPUT:7"},
		{domain.StateWaitingApproval, "", KindWaitingApproval, true, "task:tsk_1:WAITING_APPROVAL:7"},
		{domain.StateFailed, "tests failed", KindFailedTests, true, "task:tsk_1:FAILED:7"},
		{domain.StateFailed, "build_failed", KindFailedBuild, true, "task:tsk_1:FAILED:7"},
		{domain.StateFailed, "agent crashed", "", false, ""},
		{domain.StateRunning, "", "", false, ""},
	}
	for _, c := range cases {
		task.FailureReason = c.reason
		d, ok := ForTaskTransition(task, c.to)
		if ok != c.notify {
			t.Fatalf("%s/%q notify = %v, want %v", c.to, c.reason, ok, c.notify)
		}
		if !ok {
			continue
		}
		if d.Kind != c.kind || d.EventKey != c.keyWant {
			t.Fatalf("%s/%q draft = %+v", c.to, c.reason, d)
		}
		if d.URL != "/tasks/tsk_1" || d.TaskID != "tsk_1" || d.ProjectID != "prj_1" {
			t.Fatalf("draft references = %+v", d)
		}
	}
}

func TestReleaseAndUpdateDrafts(t *testing.T) {
	ok := ForRelease(domain.Release{ID: "rel_1", Version: "0.2.0", Status: domain.ReleasePublished}, "prj_1")
	if ok.Kind != KindReleaseSucceeded || ok.EventKey != "release:rel_1:published" {
		t.Fatalf("release success = %+v", ok)
	}
	bad := ForRelease(domain.Release{ID: "rel_1", Version: "0.2.0", Status: domain.ReleaseFailed}, "prj_1")
	if bad.Kind != KindReleaseFailed || bad.EventKey == ok.EventKey {
		t.Fatalf("release failure = %+v", bad)
	}
	upOK := ForUpdate("0.3.0", true)
	upBad := ForUpdate("0.3.0", false)
	if upOK.Kind != KindUpdateSucceeded || upBad.Kind != KindUpdateFailed || upOK.EventKey == upBad.EventKey {
		t.Fatalf("update drafts = %+v / %+v", upOK, upBad)
	}
}

func TestEnqueueDraftUsesEnqueue(t *testing.T) {
	env := newEnv(t, 0)
	d := ForUpdate("0.4.0", true)
	n, inserted, err := env.svc.EnqueueDraft(context.Background(), d)
	if err != nil || !inserted || n.Kind != KindUpdateSucceeded {
		t.Fatalf("enqueue draft: n=%+v inserted=%v err=%v", n, inserted, err)
	}
}
