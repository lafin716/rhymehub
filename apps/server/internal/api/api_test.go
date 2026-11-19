package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/store"
)

const testPassword = "correct horse battery"

type testEnv struct {
	t       *testing.T
	h       http.Handler
	srv     *Server
	cookies []*http.Cookie
}

func newTestEnv(t *testing.T, cfg config.Config) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "rhymehub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	srv, err := New(Options{Store: st, Config: cfg, Version: "test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return &testEnv{t: t, h: srv.Handler(), srv: srv}
}

// do sends a request from a loopback peer unless remote is set.
func (e *testEnv) do(method, path string, body any, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			e.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "application/json")
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func withCookie(c *http.Cookie) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(c) }
}

func withCSRF() func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(CSRFHeader, "1") }
}

func withBearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", SessionCookie)
	return nil
}

func decodeErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func (e *testEnv) setupAdmin(username string) {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": username, "password": testPassword})
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
}

func (e *testEnv) login(username, password string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/auth/login", map[string]string{"username": username, "password": password})
}

func TestMetaAndSetupLifecycle(t *testing.T) {
	e := newTestEnv(t, config.Default())

	rec := e.do(http.MethodGet, "/api/v1/meta", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"needsSetup":true`) {
		t.Fatalf("meta before setup: %d %s", rec.Code, rec.Body.String())
	}

	e.setupAdmin("root")
	// Setup must not create a session (web assumption 1).
	for _, c := range e.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "x", "password": testPassword}).Result().Cookies() {
		if c.Name == SessionCookie {
			t.Fatal("setup must not set a session cookie")
		}
	}

	rec = e.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "again", "password": testPassword})
	if rec.Code != http.StatusConflict || decodeErrCode(t, rec) != "conflict" {
		t.Fatalf("second setup: %d %s", rec.Code, rec.Body.String())
	}

	rec = e.do(http.MethodGet, "/api/v1/meta", nil)
	if !strings.Contains(rec.Body.String(), `"needsSetup":false`) {
		t.Fatalf("meta after setup: %s", rec.Body.String())
	}
}

func TestSetupRejectsWeakPassword(t *testing.T) {
	e := newTestEnv(t, config.Default())
	rec := e.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "root", "password": "short"})
	if rec.Code != http.StatusBadRequest || decodeErrCode(t, rec) != "bad_request" {
		t.Fatalf("weak password: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoginSessionCookieAndCSRF(t *testing.T) {
	e := newTestEnv(t, config.Default())
	e.setupAdmin("root")

	if rec := e.login("root", "wrong password!!"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad password: %d", rec.Code)
	}
	rec := e.login("root", testPassword)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	c := sessionCookieFrom(t, rec)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie flags: httpOnly=%v sameSite=%v", c.HttpOnly, c.SameSite)
	}
	if c.Secure {
		t.Fatal("plain HTTP request must not set Secure")
	}

	if rec := e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)); rec.Code != http.StatusOK {
		t.Fatalf("me with cookie: %d", rec.Code)
	}

	// Cookie-authenticated state change without the CSRF header is refused.
	rec = e.do(http.MethodPost, "/api/v1/tokens", map[string]string{"name": "laptop"}, withCookie(c))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie POST without CSRF: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/api/v1/tokens", map[string]string{"name": "laptop"}, withCookie(c), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("cookie POST with CSRF: %d %s", rec.Code, rec.Body.String())
	}

	// Logout needs CSRF too, then the session is dead.
	if rec := e.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(c)); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/api/v1/auth/logout", nil, withCookie(c), withCSRF()); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: %d", rec.Code)
	}
}

func TestBearerTokenLifecycle(t *testing.T) {
	e := newTestEnv(t, config.Default())
	e.setupAdmin("root")
	c := sessionCookieFrom(t, e.login("root", testPassword))

	rec := e.do(http.MethodPost, "/api/v1/tokens", map[string]string{"name": "cli"}, withCookie(c), withCSRF())
	var created struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || !strings.HasPrefix(created.Token, "rht_") {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}

	if rec := e.do(http.MethodGet, "/api/v1/auth/me", nil, withBearer(created.Token)); rec.Code != http.StatusOK {
		t.Fatalf("me with bearer: %d", rec.Code)
	}
	// Bearer requests carry no ambient authority, so no CSRF header is required.
	if rec := e.do(http.MethodPost, "/api/v1/tokens", map[string]string{"name": "second"}, withBearer(created.Token)); rec.Code != http.StatusCreated {
		t.Fatalf("bearer POST without CSRF: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/auth/me", nil, withBearer("rht_not-a-real-token")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown bearer: %d", rec.Code)
	}

	if rec := e.do(http.MethodDelete, "/api/v1/tokens/"+created.ID, nil, withCookie(c), withCSRF()); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/auth/me", nil, withBearer(created.Token)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer: %d", rec.Code)
	}
}

func TestSessionAndTokenStoredHashedOnly(t *testing.T) {
	e := newTestEnv(t, config.Default())
	e.setupAdmin("root")
	rec := e.login("root", testPassword)
	c := sessionCookieFrom(t, rec)
	// The raw cookie value must not be the stored token hash, and hashing must be deterministic.
	if hashToken(c.Value) == c.Value || len(hashToken(c.Value)) != 64 {
		t.Fatalf("session token hash looks wrong")
	}
	if _, err := e.srv.st.GetSession(context.Background(), c.Value); err == nil {
		t.Fatal("raw cookie value must not be a stored session key")
	}
	if _, err := e.srv.st.GetSession(context.Background(), hashToken(c.Value)); err != nil {
		t.Fatalf("hashed session key not found: %v", err)
	}
}

func TestRoleEnforcement(t *testing.T) {
	e := newTestEnv(t, config.Default())
	e.setupAdmin("root")
	adminCookie := sessionCookieFrom(t, e.login("root", testPassword))

	rec := e.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": "dev1", "password": testPassword, "role": "developer",
	}, withCookie(adminCookie), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create developer: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": "bad", "password": testPassword, "role": "superuser",
	}, withCookie(adminCookie), withCSRF()); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown role: %d", rec.Code)
	}

	devCookie := sessionCookieFrom(t, e.login("dev1", testPassword))
	if rec := e.do(http.MethodGet, "/api/v1/users", nil, withCookie(devCookie)); rec.Code != http.StatusForbidden {
		t.Fatalf("developer listing users: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/users", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous listing users: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/users", nil, withCookie(adminCookie)); rec.Code != http.StatusOK {
		t.Fatalf("admin listing users: %d", rec.Code)
	}
}

func TestAdminCannotDemoteSelf(t *testing.T) {
	e := newTestEnv(t, config.Default())
	e.setupAdmin("root")
	c := sessionCookieFrom(t, e.login("root", testPassword))
	me := e.do(http.MethodGet, "/api/v1/auth/me", nil, withCookie(c))
	var out struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	rec := e.do(http.MethodPatch, "/api/v1/users/"+out.User.ID, map[string]string{"role": "developer"},
		withCookie(c), withCSRF())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self demotion: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTLSPolicy(t *testing.T) {
	cfg := config.Default()
	cfg.InsecureAllowedCIDRs = []string{"100.64.0.0/10"}
	e := newTestEnv(t, cfg)

	plain := func(remote string) *httptest.ResponseRecorder {
		return e.do(http.MethodGet, "/api/v1/meta", nil, func(r *http.Request) { r.RemoteAddr = remote })
	}
	if rec := plain("192.168.1.20:4000"); rec.Code != http.StatusForbidden || decodeErrCode(t, rec) != "tls_required" {
		t.Fatalf("LAN plain HTTP: %d %s", rec.Code, rec.Body.String())
	}
	if rec := plain("100.100.10.5:4000"); rec.Code != http.StatusOK {
		t.Fatalf("tailscale plain HTTP (allow-listed): %d", rec.Code)
	}
	if rec := plain("127.0.0.1:4000"); rec.Code != http.StatusOK {
		t.Fatalf("loopback plain HTTP: %d", rec.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newTestEnv(t, config.Default())
	e.setupAdmin("root")
	for i := 0; i < 5; i++ {
		if rec := e.login("root", "wrong password!!"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	rec := e.login("root", testPassword)
	if rec.Code != http.StatusTooManyRequests || decodeErrCode(t, rec) != "rate_limited" {
		t.Fatalf("sixth attempt: %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownAPIPathIsJSON404(t *testing.T) {
	e := newTestEnv(t, config.Default())
	rec := e.do(http.MethodGet, "/api/v1/nope", nil)
	if rec.Code != http.StatusNotFound || decodeErrCode(t, rec) != "not_found" {
		t.Fatalf("unknown API path: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardSPAFallback(t *testing.T) {
	e := newTestEnv(t, config.Default())
	rec := e.do(http.MethodGet, "/projects/abc", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("SPA route: %d", rec.Code)
	}
	rec = e.do(http.MethodGet, "/missing.js", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset must not fall back to HTML: %d", rec.Code)
	}
}
