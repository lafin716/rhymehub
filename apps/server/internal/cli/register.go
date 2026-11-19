package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lafin716/rhymehub/internal/domain"
	"github.com/lafin716/rhymehub/internal/store"
	"github.com/lafin716/rhymehub/internal/workspace"
)

// npmDefaultTest is the placeholder npm init writes. It is not a real test command.
const npmDefaultTest = `echo "Error: no test specified" && exit 1`

// detectStack inspects the repository root and returns the stack name and the test command the project
// should run. The first marker file wins: package.json, go.mod, then gradlew. An unknown layout is "generic"
// with no test command. Nothing is executed.
func detectStack(dir string) (stack, testCmd string) {
	if pkg, ok := readPackageJSON(filepath.Join(dir, "package.json")); ok {
		if test := strings.TrimSpace(pkg.Scripts["test"]); test != "" && test != npmDefaultTest {
			return "node", "npm test"
		}
		return "node", ""
	}
	if fileExists(filepath.Join(dir, "go.mod")) {
		return "go", "go test ./..."
	}
	if fileExists(filepath.Join(dir, "gradlew")) {
		return "gradle", "./gradlew test"
	}
	return "generic", ""
}

type packageJSON struct {
	Scripts map[string]string `json:"scripts"`
}

// readPackageJSON parses package.json. A malformed file still counts as a node project with no test script.
func readPackageJSON(path string) (packageJSON, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return packageJSON{}, false
	}
	var pkg packageJSON
	_ = json.Unmarshal(b, &pkg)
	return pkg, true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// isLoopbackHost reports whether host (an IP or name, without port) only accepts local connections.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runRegister adds a git repository as a project. With --server and RHYMEHUB_TOKEN it registers the project on
// a remote server; otherwise it writes to the local store under RHYMEHUB_HOME. The repository is not modified.
func runRegister(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	pathFlag := fs.String("path", "", "repository root (default: current directory)")
	nameFlag := fs.String("name", "", "project name (default: directory name)")
	serverFlag := fs.String("server", "", "register on a remote RhymeHub server (uses RHYMEHUB_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	dir := *pathFlag
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(env.Stderr, "register:", err)
			return ExitFailure
		}
		dir = wd
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(env.Stderr, "register:", err)
		return ExitFailure
	}
	// Resolve symlinks so the stored repo path matches what git reports (macOS temp dirs live under /var).
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if err := workspace.ValidateRepo(ctx, dir); err != nil {
		fmt.Fprintf(env.Stderr, "register: %s is not a git repository root: %v\n", dir, err)
		return ExitFailure
	}
	name := strings.TrimSpace(*nameFlag)
	if name == "" {
		name = filepath.Base(dir)
	}
	stack, testCmd := detectStack(dir)
	branch, err := workspace.DefaultBranch(ctx, dir)
	if err != nil || branch == "" {
		branch = "main"
	}

	if *serverFlag != "" {
		return registerRemote(ctx, env, *serverFlag, map[string]any{
			"name":          name,
			"repoPath":      dir,
			"defaultBranch": branch,
			"testCmd":       testCmd,
		})
	}

	p, err := resolvePaths(env)
	if err != nil {
		fmt.Fprintln(env.Stderr, "register:", err)
		return ExitFailure
	}
	if _, err := os.Stat(p.DB()); err != nil {
		fmt.Fprintln(env.Stderr, "register: database not found; run rhymehub init first")
		return ExitFailure
	}
	st, err := store.Open(p.DB())
	if err != nil {
		fmt.Fprintln(env.Stderr, "register: open database:", err)
		return ExitFailure
	}
	defer st.Close()

	existing, err := st.ListProjects(ctx, true)
	if err != nil {
		fmt.Fprintln(env.Stderr, "register: list projects:", err)
		return ExitFailure
	}
	for _, pr := range existing {
		if filepath.Clean(pr.RepoPath) == dir {
			fmt.Fprintf(env.Stderr, "register: %s is already registered as project %s\n", dir, pr.ID)
			return ExitFailure
		}
	}
	created, err := st.CreateProject(ctx, domain.Project{
		Name:          name,
		RepoPath:      dir,
		DefaultBranch: branch,
		Stack:         stack,
		TestCmd:       testCmd,
	})
	if err != nil {
		fmt.Fprintln(env.Stderr, "register: create project:", err)
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "registered project %s (%s) stack=%s testCmd=%q\n", created.ID, created.Name, stack, testCmd)
	return ExitOK
}

// registerRemote posts the project to POST /api/v1/projects. The token is read from the environment only and is
// never printed.
func registerRemote(ctx context.Context, env Env, server string, body map[string]any) int {
	token := os.Getenv("RHYMEHUB_TOKEN")
	if token == "" {
		fmt.Fprintln(env.Stderr, "register: --server requires RHYMEHUB_TOKEN to be set")
		return ExitUsage
	}
	u, err := url.Parse(server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fmt.Fprintln(env.Stderr, "register: --server must be an http(s) URL")
		return ExitUsage
	}
	if u.User != nil {
		fmt.Fprintln(env.Stderr, "register: --server must not contain credentials")
		return ExitUsage
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		fmt.Fprintln(env.Stderr, "warning: --server uses plain HTTP; the token will be sent unencrypted")
	}
	// The body holds only project fields; the token goes in the Authorization header alone.
	payload, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintln(env.Stderr, "register:", err)
		return ExitFailure
	}
	endpoint := strings.TrimRight(server, "/") + "/api/v1/projects"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(env.Stderr, "register:", err)
		return ExitFailure
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(env.Stderr, "register: request failed:", err)
		return ExitFailure
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		fmt.Fprintf(env.Stderr, "register: server returned %d: %s\n", resp.StatusCode, apiErrorMessage(raw))
		return ExitFailure
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		fmt.Fprintln(env.Stderr, "register: unexpected server response")
		return ExitFailure
	}
	fmt.Fprintf(env.Stdout, "registered project %s (%s) on %s\n", created.ID, created.Name, u.Host)
	return ExitOK
}

// apiErrorMessage extracts the {"error":{"message"}} envelope when present and otherwise returns a short
// fixed description, so arbitrary response bodies are not echoed.
func apiErrorMessage(raw []byte) string {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Code != "" {
		return env.Error.Code + ": " + env.Error.Message
	}
	return "no error details"
}
