package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lafin716/rhymehub/internal/config"
	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
)

// setupProjectEnv starts a server whose data directory is a temporary RHYMEHUB_HOME, creates the admin and
// returns its session cookie. Unlike newTestEnv it passes Paths, which gitUrl cloning needs.
func setupProjectEnv(t *testing.T) (*testEnv, *http.Cookie) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	st, err := store.Open(filepath.Join(t.TempDir(), "rhymehub.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	srv, err := New(Options{Store: st, Config: config.Default(), Paths: config.NewPaths(home), Version: "test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	e := &testEnv{t: t, h: srv.Handler(), srv: srv}
	e.setupAdmin("root")
	return e, sessionCookieFrom(t, e.login("root", testPassword))
}

// roleCookie creates a user with the given role through the admin API and returns that user's session cookie.
func roleCookie(t *testing.T, e *testEnv, admin *http.Cookie, username string, role domain.Role) *http.Cookie {
	t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/users", map[string]string{
		"username": username, "password": testPassword, "role": string(role),
	}, withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", role, rec.Code, rec.Body.String())
	}
	return sessionCookieFrom(t, e.login(username, testPassword))
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false"}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initGitRepo creates a small Go-shaped git repository with one commit in dir.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "init")
}

// createRepoProject registers repo through the API and returns the new project id.
func createRepoProject(t *testing.T, e *testEnv, cookie *http.Cookie, repo string) string {
	t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/projects", map[string]string{"name": "demo", "repoPath": repo},
		withCookie(cookie), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", rec.Code, rec.Body.String())
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.ID == "" {
		t.Fatalf("decode project: %v %s", err, rec.Body.String())
	}
	return p.ID
}

func TestProjectRoleChecks(t *testing.T) {
	e, admin := setupProjectEnv(t)
	dev := roleCookie(t, e, admin, "dev1", domain.RoleDeveloper)
	maint := roleCookie(t, e, admin, "maint1", domain.RoleMaintainer)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)

	// Anonymous callers are refused on every project route.
	if rec := e.do(http.MethodGet, "/api/v1/projects", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/api/v1/projects", map[string]string{"name": "x", "repoPath": repo}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d", rec.Code)
	}

	// Any authenticated user can read.
	if rec := e.do(http.MethodGet, "/api/v1/projects", nil, withCookie(dev)); rec.Code != http.StatusOK {
		t.Fatalf("developer list: %d %s", rec.Code, rec.Body.String())
	}

	// Policy fields on create need the maintainer role.
	rec := e.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"name": "policy", "repoPath": repo, "testCmd": "make test",
	}, withCookie(dev), withCSRF())
	if rec.Code != http.StatusForbidden || decodeErrCode(t, rec) != "forbidden" {
		t.Fatalf("developer create with policy: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"name": "policy", "repoPath": repo, "testCmd": "make test",
	}, withCookie(maint), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("maintainer create with policy: %d %s", rec.Code, rec.Body.String())
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}

	// Policy fields on patch need the maintainer role; developers get 403.
	rec = e.do(http.MethodPatch, "/api/v1/projects/"+made.ID, map[string]any{"testCmd": "go test ./..."},
		withCookie(dev), withCSRF())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("developer patch policy: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(http.MethodPatch, "/api/v1/projects/"+made.ID, map[string]any{"testCmd": "go test ./..."},
		withCookie(maint), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("maintainer patch policy: %d %s", rec.Code, rec.Body.String())
	}

	// Non-policy patch is allowed for developers.
	rec = e.do(http.MethodPatch, "/api/v1/projects/"+made.ID, map[string]any{"name": "renamed"},
		withCookie(dev), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("developer patch name: %d %s", rec.Code, rec.Body.String())
	}

	// Keys need the maintainer role.
	if rec := e.do(http.MethodGet, "/api/v1/projects/"+made.ID+"/keys", nil, withCookie(dev)); rec.Code != http.StatusForbidden {
		t.Fatalf("developer list keys: %d", rec.Code)
	}
	if rec := e.do(http.MethodPost, "/api/v1/projects/"+made.ID+"/keys", map[string]string{"name": "k"},
		withCookie(dev), withCSRF()); rec.Code != http.StatusForbidden {
		t.Fatalf("developer create key: %d", rec.Code)
	}
}

func TestCreateProjectFromRepoPath(t *testing.T) {
	e, admin := setupProjectEnv(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)

	rec := e.do(http.MethodPost, "/api/v1/projects", map[string]string{"name": "demo", "repoPath": repo},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var p domain.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.ID, "prj_") || p.RepoPath != repo {
		t.Fatalf("unexpected project: %+v", p)
	}
	if p.Stack != "go" || p.TestCmd != "go test ./..." {
		t.Fatalf("detection not applied: stack=%q testCmd=%q", p.Stack, p.TestCmd)
	}

	audit := e.do(http.MethodGet, "/api/v1/audit", nil, withCookie(admin))
	if !strings.Contains(audit.Body.String(), "project.create") {
		t.Fatalf("project.create not audited: %s", audit.Body.String())
	}

	// GET by id reports stats and the git block.
	rec = e.do(http.MethodGet, "/api/v1/projects/"+p.ID, nil, withCookie(admin))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"git"`) {
		t.Fatalf("get project: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/projects/prj_missing", nil, withCookie(admin)); rec.Code != http.StatusNotFound {
		t.Fatalf("missing project: %d", rec.Code)
	}
}

func TestCreateProjectValidation(t *testing.T) {
	e, admin := setupProjectEnv(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)
	notGit := t.TempDir()

	cases := []struct {
		name string
		body map[string]string
	}{
		{"relative repoPath", map[string]string{"name": "x", "repoPath": "relative/path"}},
		{"non-git directory", map[string]string{"name": "x", "repoPath": notGit}},
		{"missing name", map[string]string{"repoPath": repo}},
		{"neither repoPath nor gitUrl", map[string]string{"name": "x"}},
		{"both repoPath and gitUrl", map[string]string{"name": "x", "repoPath": repo, "gitUrl": "https://example.com/a.git"}},
		{"gitUrl option injection", map[string]string{"name": "x", "gitUrl": "--upload-pack=x"}},
		{"gitUrl ext transport", map[string]string{"name": "x", "gitUrl": "ext::sh"}},
	}
	for _, tc := range cases {
		rec := e.do(http.MethodPost, "/api/v1/projects", tc.body, withCookie(admin), withCSRF())
		if rec.Code != http.StatusBadRequest || decodeErrCode(t, rec) != "bad_request" {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestDiscoverProjects(t *testing.T) {
	e, admin := setupProjectEnv(t)
	root := t.TempDir()
	initGitRepo(t, filepath.Join(root, "a"))
	initGitRepo(t, filepath.Join(root, "nested", "app"))
	initGitRepo(t, filepath.Join(root, ".hidden", "repo"))
	initGitRepo(t, filepath.Join(root, "node_modules", "pkg"))
	registeredPath := filepath.Join(root, "a")
	createRepoProject(t, e, admin, registeredPath)

	rec := e.do(http.MethodPost, "/api/v1/projects/discover", map[string]string{"root": root},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusOK {
		t.Fatalf("discover: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Candidates []discoverCandidate `json:"candidates"`
		Truncated  bool                `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	found := map[string]discoverCandidate{}
	for _, c := range out.Candidates {
		found[c.Path] = c
	}
	if c, ok := found[registeredPath]; !ok || !c.Registered {
		t.Fatalf("registered repo not reported as registered: %+v", out.Candidates)
	}
	if _, ok := found[filepath.Join(root, "nested", "app")]; !ok {
		t.Fatalf("depth-2 repo not discovered: %+v", out.Candidates)
	}
	for path := range found {
		if strings.Contains(path, ".hidden") || strings.Contains(path, "node_modules") {
			t.Fatalf("hidden or node_modules repo discovered: %s", path)
		}
	}

	if rec := e.do(http.MethodPost, "/api/v1/projects/discover", map[string]string{"root": "relative"},
		withCookie(admin), withCSRF()); rec.Code != http.StatusBadRequest {
		t.Fatalf("relative root: %d", rec.Code)
	}
}

func TestProjectKeysCreateListRevoke(t *testing.T) {
	e, admin := setupProjectEnv(t)
	maint := roleCookie(t, e, admin, "maint1", domain.RoleMaintainer)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)
	projectID := createRepoProject(t, e, maint, repo)
	keysPath := "/api/v1/projects/" + projectID + "/keys"

	rec := e.do(http.MethodPost, keysPath, map[string]string{"name": "ci"}, withCookie(maint), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Key, sdkKeyPrefix) || created.ID == "" {
		t.Fatalf("raw key not returned once: %s", rec.Body.String())
	}

	rec = e.do(http.MethodGet, keysPath, nil, withCookie(maint))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("list keys: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), created.Key) || strings.Contains(rec.Body.String(), hashToken(created.Key)) {
		t.Fatalf("key material leaked in list: %s", rec.Body.String())
	}

	revokePath := keysPath + "/" + created.ID
	if rec := e.do(http.MethodDelete, revokePath, nil, withCookie(maint), withCSRF()); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodDelete, revokePath, nil, withCookie(maint), withCSRF()); rec.Code != http.StatusConflict {
		t.Fatalf("second revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodDelete, keysPath+"/key_missing", nil, withCookie(maint), withCSRF()); rec.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown key: %d", rec.Code)
	}

	audit := e.do(http.MethodGet, "/api/v1/audit", nil, withCookie(admin))
	if !strings.Contains(audit.Body.String(), "key.create") || !strings.Contains(audit.Body.String(), "key.revoke") {
		t.Fatalf("key actions not audited: %s", audit.Body.String())
	}
}

func TestProjectGitEndpoint(t *testing.T) {
	e, admin := setupProjectEnv(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)
	projectID := createRepoProject(t, e, admin, repo)

	rec := e.do(http.MethodGet, "/api/v1/projects/"+projectID+"/git", nil, withCookie(admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("git: %d %s", rec.Code, rec.Body.String())
	}
	var g projectGitDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	if !g.Available || g.Dirty || g.Head == "" || len(g.RecentCommits) != 1 || g.RecentCommits[0].Subject != "init" {
		t.Fatalf("unexpected git state: %+v", g)
	}

	// A broken repository must not fail the request: the endpoint reports available=false.
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	rec = e.do(http.MethodGet, "/api/v1/projects/"+projectID+"/git", nil, withCookie(admin))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Fatalf("broken repo: %d %s", rec.Code, rec.Body.String())
	}
}
