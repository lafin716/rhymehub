package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lafin716/rhymehub/apps/server/sdkdist"
	"github.com/lafin716/rhymehub/internal/domain"
)

type sdkPromptBody struct {
	ProjectID    string             `json:"projectId"`
	ServerURL    string             `json:"serverUrl"`
	SDKVersion   string             `json:"sdkVersion"`
	SDKAvailable bool               `json:"sdkAvailable"`
	Packages     []sdkPromptPackage `json:"packages"`
	Prompt       string             `json:"prompt"`
	Skill        string             `json:"skill"`
	SkillPath    string             `json:"skillPath"`
}

func TestSDKPromptRendersProjectValuesWithoutSecrets(t *testing.T) {
	e, admin := setupProjectEnv(t)
	repo := filepath.Join(t.TempDir(), "app")
	initGitRepo(t, repo)
	id := createRepoProject(t, e, admin, repo)

	// A live key must never appear in the prompt.
	rec := e.do(http.MethodPost, "/api/v1/projects/"+id+"/keys", map[string]any{"name": "web"},
		withCookie(admin), withCSRF())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", rec.Code, rec.Body.String())
	}
	var key struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &key)

	reporter := roleCookie(t, e, admin, "rep1", domain.RoleReporter)
	rec = e.do(http.MethodGet, "/api/v1/projects/"+id+"/sdk-prompt", nil, withCookie(reporter))
	if rec.Code != http.StatusOK {
		t.Fatalf("sdk-prompt: %d %s", rec.Code, rec.Body.String())
	}
	var b sdkPromptBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.ProjectID != id || b.ServerURL != "http://example.com" || b.SDKVersion != sdkdist.Version() {
		t.Fatalf("unexpected header fields: %+v", b)
	}
	if b.SkillPath != ".claude/skills/rhymehub-sdk/SKILL.md" {
		t.Fatalf("skillPath = %q", b.SkillPath)
	}
	if len(b.Packages) != 3 {
		t.Fatalf("packages = %+v", b.Packages)
	}
	for _, p := range b.Packages {
		if p.URL != "http://example.com/api/v1/sdk/dist/"+p.File || !strings.Contains(b.Prompt, p.URL) {
			t.Fatalf("package url %q missing from prompt", p.URL)
		}
	}
	if !strings.HasPrefix(b.Skill, "---\nname: rhymehub-sdk\n") {
		t.Fatalf("skill frontmatter: %q", b.Skill[:60])
	}
	for _, text := range []string{b.Prompt, b.Skill} {
		if !strings.Contains(text, "VITE_RHYMEHUB_PROJECT="+id) {
			t.Fatal("prompt does not carry the project id")
		}
		if strings.Contains(text, key.Key) || strings.Contains(text, "{{") {
			t.Fatal("prompt leaks the key or has unrendered template fields")
		}
	}
}

func TestSDKPromptServerURLOverride(t *testing.T) {
	e, admin := setupProjectEnv(t)
	repo := filepath.Join(t.TempDir(), "app")
	initGitRepo(t, repo)
	id := createRepoProject(t, e, admin, repo)
	base := "/api/v1/projects/" + id + "/sdk-prompt?serverUrl="

	rec := e.do(http.MethodGet, base+"https://rich.tail.ts.net:7780/", nil, withCookie(admin))
	if rec.Code != http.StatusOK {
		t.Fatalf("override: %d %s", rec.Code, rec.Body.String())
	}
	var b sdkPromptBody
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if b.ServerURL != "https://rich.tail.ts.net:7780" || !strings.Contains(b.Prompt, "https://rich.tail.ts.net:7780/api/v1/sdk/dist/") {
		t.Fatalf("serverUrl not applied: %q", b.ServerURL)
	}

	for _, bad := range []string{"javascript:alert(1)", "ftp://host", "http://host/path", "http://u:p@host",
		"http://host?x=1", "http://ho%20st"} {
		rec := e.do(http.MethodGet, base+bad, nil, withCookie(admin))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("serverUrl %q: want 400, got %d", bad, rec.Code)
		}
	}

	if rec := e.do(http.MethodGet, "/api/v1/projects/"+id+"/sdk-prompt", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: want 401, got %d", rec.Code)
	}
	if rec := e.do(http.MethodGet, "/api/v1/projects/nope/sdk-prompt", nil, withCookie(admin)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: want 404, got %d", rec.Code)
	}
}

func TestSDKDistServesOnlyTarballNames(t *testing.T) {
	e, _ := setupProjectEnv(t)
	for _, name := range []string{"..%2Fgo.mod", ".keep", "rhymehub-evil-1.0.0.tgz", "rhymehub-react-9.9.9.tgz"} {
		rec := e.do(http.MethodGet, "/api/v1/sdk/dist/"+name, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", name, rec.Code)
		}
	}
	if !sdkdist.Available() {
		t.Skip("SDK tarballs are not embedded (run `make web` first)")
	}
	for _, p := range sdkdist.Packages() {
		rec := e.do(http.MethodGet, "/api/v1/sdk/dist/"+p.File, nil) // public: no session needed
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/gzip" || rec.Body.Len() == 0 {
			t.Fatalf("%s: %d %q", p.File, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
