package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lafin716/rhymehub/internal/domain"
)

func TestReleaseListAndGet(t *testing.T) {
	e, admin := setupProjectEnv(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)
	projectID := createRepoProject(t, e, admin, repo)

	rec := e.do(http.MethodGet, "/api/v1/releases?projectId="+projectID, nil, withCookie(admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("list releases: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Releases []domain.Release `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Releases == nil {
		t.Fatalf("releases must be an array: %v %s", err, rec.Body.String())
	}

	if rec := e.do(http.MethodGet, "/api/v1/releases", nil, withCookie(admin)); rec.Code != http.StatusBadRequest {
		t.Fatalf("list without projectId: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/releases?projectId=prj_missing", nil, withCookie(admin)); rec.Code != http.StatusNotFound {
		t.Fatalf("list for missing project: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/releases/rel_missing", nil, withCookie(admin)); rec.Code != http.StatusNotFound {
		t.Fatalf("get missing release: %d", rec.Code)
	}

	// Releases are readable by any authenticated user.
	dev := roleCookie(t, e, admin, "dev1", domain.RoleDeveloper)
	if rec := e.do(http.MethodGet, "/api/v1/releases?projectId="+projectID, nil, withCookie(dev)); rec.Code != http.StatusOK {
		t.Fatalf("developer list releases: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/releases?projectId="+projectID, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list releases: %d", rec.Code)
	}
}

func TestManualReleaseRoleAndAvailability(t *testing.T) {
	e, admin := setupProjectEnv(t)
	dev := roleCookie(t, e, admin, "dev1", domain.RoleDeveloper)
	maint := roleCookie(t, e, admin, "maint1", domain.RoleMaintainer)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)
	projectID := createRepoProject(t, e, admin, repo)
	path := "/api/v1/projects/" + projectID + "/releases"

	if rec := e.do(http.MethodPost, path, map[string]string{"version": "1.0.0"}, withCookie(dev), withCSRF()); rec.Code != http.StatusForbidden {
		t.Fatalf("developer manual release: %d %s", rec.Code, rec.Body.String())
	}
	// The test server has no project releaser wired, so a maintainer gets 503 rather than a silent no-op.
	rec := e.do(http.MethodPost, path, map[string]string{"version": "1.0.0"}, withCookie(maint), withCSRF())
	if rec.Code != http.StatusServiceUnavailable || decodeErrCode(t, rec) != "unavailable" {
		t.Fatalf("maintainer manual release without releaser: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodPost, path, map[string]string{"version": "1.0.0"}, withCookie(maint)); rec.Code != http.StatusForbidden {
		t.Fatalf("manual release without CSRF: %d", rec.Code)
	}
}

func TestAuditIsAdminOnly(t *testing.T) {
	e, admin := setupProjectEnv(t)
	maint := roleCookie(t, e, admin, "maint1", domain.RoleMaintainer)
	repo := filepath.Join(t.TempDir(), "repo")
	initGitRepo(t, repo)
	createRepoProject(t, e, maint, repo)

	if rec := e.do(http.MethodGet, "/api/v1/audit", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous audit: %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/audit", nil, withCookie(maint)); rec.Code != http.StatusForbidden {
		t.Fatalf("maintainer audit: %d", rec.Code)
	}
	rec := e.do(http.MethodGet, "/api/v1/audit?limit=10", nil, withCookie(admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin audit: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"entries"`) || !strings.Contains(rec.Body.String(), "project.create") {
		t.Fatalf("audit entries missing project.create: %s", rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/audit?before=-1", nil, withCookie(admin)); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid before cursor: %d", rec.Code)
	}
}
