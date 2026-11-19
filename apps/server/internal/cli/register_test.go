package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectStack(t *testing.T) {
	cases := []struct {
		name      string
		files     map[string]string
		wantStack string
		wantTest  string
	}{
		{"node with test script", map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`}, "node", "npm test"},
		{"node npm default placeholder", map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, "node", ""},
		{"node without scripts", map[string]string{"package.json": `{}`}, "node", ""},
		{"node malformed json", map[string]string{"package.json": `{not json`}, "node", ""},
		{"go module", map[string]string{"go.mod": "module x\n"}, "go", "go test ./..."},
		{"gradle wrapper", map[string]string{"gradlew": "#!/bin/sh\n"}, "gradle", "./gradlew test"},
		{"unknown layout", map[string]string{"README.md": "hi"}, "generic", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, content := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			stack, testCmd := detectStack(dir)
			if stack != tc.wantStack || testCmd != tc.wantTest {
				t.Fatalf("detectStack = (%q, %q), want (%q, %q)", stack, testCmd, tc.wantStack, tc.wantTest)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "::1": true,
		"100.64.0.5": false, "192.168.1.10": false, "example.ts.net": false, "": false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %t, want %t", host, got, want)
		}
	}
}

func TestRegisterLocalCreatesProjectOnce(t *testing.T) {
	requireGit(t)
	repo := makeGitRepo(t, map[string]string{"go.mod": "module demo\n"})
	env, out, errb := initTestHome(t)

	if code := runRegister(context.Background(), env, []string{"--path", repo}); code != ExitOK {
		t.Fatalf("register exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "stack=go") {
		t.Fatalf("stdout %q", out.String())
	}
	// Registering the same repository again must fail rather than create a duplicate.
	errb.Reset()
	if code := runRegister(context.Background(), env, []string{"--path", repo}); code != ExitFailure {
		t.Fatalf("duplicate register exit %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errb.String(), "already registered") {
		t.Fatalf("stderr %q", errb.String())
	}
	// The repository itself must not be modified.
	if _, err := os.Stat(filepath.Join(repo, ".rhymehub")); err == nil {
		t.Fatal("register wrote into the repository")
	}
}

func TestRegisterRejectsNonRepository(t *testing.T) {
	env, _, errb := initTestHome(t)
	if code := runRegister(context.Background(), env, []string{"--path", t.TempDir()}); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errb.String(), "not a git repository root") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestRegisterRemoteRequiresToken(t *testing.T) {
	requireGit(t)
	repo := makeGitRepo(t, map[string]string{"README.md": "x"})
	t.Setenv("RHYMEHUB_TOKEN", "")
	env, _, errb := testEnv(t)
	if code := runRegister(context.Background(), env, []string{"--path", repo, "--server", "https://host.ts.net"}); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errb.String(), "RHYMEHUB_TOKEN") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestRegisterRemoteRejectsCredentialsInURL(t *testing.T) {
	requireGit(t)
	repo := makeGitRepo(t, map[string]string{"README.md": "x"})
	t.Setenv("RHYMEHUB_TOKEN", "rht_test_value_not_real_0000")
	env, _, errb := testEnv(t)
	if code := runRegister(context.Background(), env, []string{"--path", repo, "--server", "https://user:pw@host.ts.net"}); code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if strings.Contains(errb.String(), "pw") {
		t.Fatalf("stderr leaked credentials: %q", errb.String())
	}
}

func TestRegisterRemotePostsWithBearerAndHidesToken(t *testing.T) {
	requireGit(t)
	repo := makeGitRepo(t, map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`})
	const token = "rht_test_value_not_real_0000"
	t.Setenv("RHYMEHUB_TOKEN", token)

	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"prj_1","name":"demo"}`))
	}))
	defer srv.Close()

	env, out, errb := testEnv(t)
	if code := runRegister(context.Background(), env, []string{"--path", repo, "--server", srv.URL}); code != ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["testCmd"] != "npm test" {
		t.Fatalf("body testCmd = %v", gotBody["testCmd"])
	}
	if _, ok := gotBody["token"]; ok {
		t.Fatal("token field sent in request body")
	}
	if strings.Contains(out.String()+errb.String(), token) {
		t.Fatal("token printed to output")
	}
}

func TestRegisterRemoteServerErrorDoesNotEchoBody(t *testing.T) {
	requireGit(t)
	repo := makeGitRepo(t, map[string]string{"README.md": "x"})
	t.Setenv("RHYMEHUB_TOKEN", "rht_test_value_not_real_0000")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
	}))
	defer srv.Close()

	env, _, errb := testEnv(t)
	if code := runRegister(context.Background(), env, []string{"--path", repo, "--server", srv.URL}); code != ExitFailure {
		t.Fatalf("exit %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(errb.String(), "unauthorized") {
		t.Fatalf("stderr %q", errb.String())
	}
}

func TestApiErrorMessageFallback(t *testing.T) {
	if got := apiErrorMessage([]byte("<html>proxy page</html>")); got != "no error details" {
		t.Fatalf("fallback = %q", got)
	}
}
